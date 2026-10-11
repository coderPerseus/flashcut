// Package clef calls Cloudflare's Clef decision models (API-compatible with
// TypeSafe's Jev): a state plus typed questions in, calibrated answers out.
package clef

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Models.
const (
	// Omni judges video with its soundtrack.
	Omni = "@cf/cloudflare/clef-omni"
	// Flash is the fast, cheap text model used to understand chat.
	Flash = "@cf/cloudflare/clef-flash"
)

// PricePerMTok is the input price in US dollars per million tokens.
var PricePerMTok = map[string]float64{Omni: 0.15, Flash: 0.038}

// Question is a typed question. Use Noul, Choice or Score to build one.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Noul asks a yes/no question; the answer is the probability of yes.
func Noul(instructions string, yes, no string) Question {
	q := Question{Type: "noul", Instructions: instructions}
	if yes != "" || no != "" {
		q.Criteria = map[string]string{"true": yes, "false": no}
	}
	return q
}

// Choice picks one of options (id → description).
func Choice(instructions string, options map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: options}
}

// Levels is the number of levels of a score question.
func (q Question) Levels() int {
	switch c := q.Criteria.(type) {
	case []string:
		return len(c)
	case []any:
		return len(c)
	}
	return 0
}

// Options lists the option ids of a choice question.
func (q Question) Options() []string {
	var out []string
	switch c := q.Criteria.(type) {
	case map[string]string:
		for k := range c {
			out = append(out, k)
		}
	case map[string]any:
		for k := range c {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Score rates on ordered levels, lowest first.
func Score(instructions string, levels ...string) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: levels}
}

// Request is one model call.
type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
	// Videos are MP4 files (at most 2, 16 MiB and 60 s each; Omni only).
	Videos [][]byte `json:"-"`
	// Images are JPEG files (at most 4).
	Images [][]byte `json:"-"`
}

// Answer is one answer. Value is the yes probability for noul, the
// weighted level for score, and the chosen option's probability for choice.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Score         float64            `json:"score"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// Response is the model output.
type Response struct {
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens int `json:"input_tokens"`
	} `json:"usage"`
}

// Config holds Cloudflare credentials.
type Config struct {
	// Mode is "auto" (the signed-in cf CLI if there is one, else the
	// token), "cli" or "token".
	Mode      string `json:"mode,omitempty"`
	AccountID string `json:"accountId"`
	APIToken  string `json:"apiToken"`
}

// Client runs Clef models.
type Client struct {
	cfg  Config
	cf   string
	http *http.Client
}

// New returns a client for cfg, or an error saying how to sign in.
func New(cfg Config) (*Client, error) {
	c := &Client{cfg: cfg, http: &http.Client{Timeout: 3 * time.Minute}}
	hasToken := cfg.AccountID != "" && cfg.APIToken != ""
	switch cfg.Mode {
	case "token":
		if !hasToken {
			return nil, errors.New("请在模型设置里填写 Cloudflare Account ID 和 API Token")
		}
		return c, nil
	case "cli":
		cf, err := LookCF()
		if err != nil {
			return nil, errors.New("本机没有找到 cf 命令行；安装后运行 cf auth login，或改用 API Token")
		}
		c.cf = cf
		return c, nil
	}
	// Auto: a signed-in CLI first, then the token.
	if cf, err := LookCF(); err == nil {
		c.cf = cf
		return c, nil
	}
	if hasToken {
		return c, nil
	}
	return nil, errors.New("还没有连接 Cloudflare：在模型设置里填入 Account ID 和 API Token，或者在终端运行 cf auth login")
}

// Name says how the client reaches Cloudflare.
func (c *Client) Name() string {
	if c.cf != "" {
		return "cf CLI"
	}
	return "API Token"
}

// Run calls model with req, retrying rate limits and server errors.
func (c *Client) Run(ctx context.Context, model string, req Request) (*Response, error) {
	body := map[string]any{
		"model":     strings.TrimPrefix(model, "@cf/cloudflare/"),
		"state":     req.State,
		"questions": req.Questions,
	}
	for _, img := range req.Images {
		body["images"] = append(asStrings(body["images"]), "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(img))
	}
	if len(req.Videos) > 0 {
		var vids []string
		for _, v := range req.Videos {
			vids = append(vids, "data:video/mp4;base64,"+base64.StdEncoding.EncodeToString(v))
		}
		body["videos"] = vids
	}
	out, err := c.RunRaw(ctx, model, body)
	if err != nil {
		return nil, err
	}
	return parse(out)
}

func asStrings(v any) []string {
	s, _ := v.([]string)
	return s
}

// RunRaw calls any Workers AI model with body and returns the model's
// output (unwrapped from the REST envelope), retrying rate limits and
// server errors.
func (c *Client) RunRaw(ctx context.Context, model string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := range 4 {
		var out []byte
		var retry bool
		if c.cf != "" {
			out, retry, err = c.runCLI(ctx, model, payload)
		} else {
			out, retry, err = c.runREST(ctx, model, payload)
		}
		if err == nil {
			return unwrap(out)
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(time.Duration(2<<attempt) * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (c *Client) runREST(ctx context.Context, model string, payload []byte) ([]byte, bool, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai/run/%s", c.cfg.AccountID, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("Cloudflare 返回 HTTP %d", resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, fmt.Errorf("Cloudflare 拒绝了凭证（HTTP %d），请检查 Account ID 和 API Token 是否有 Workers AI 权限", resp.StatusCode)
	}
	return body, false, nil
}

func (c *Client) runCLI(ctx context.Context, model string, payload []byte) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "flashcut-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "body.json")
	if err := os.WriteFile(file, payload, 0o600); err != nil {
		return nil, false, err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.cf, "ai", "run", model, "--body", "@"+file)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Apps start in "/" with a minimal PATH; cf is a Node script and keeps a
	// cache in its working directory.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(c.cf)+":"+os.Getenv("PATH")+":/opt/homebrew/bin:/usr/local/bin")
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String() + stdout.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, true, fmt.Errorf("cf ai run: %w: %s", err, msg)
	}
	return stdout.Bytes(), false, nil
}

// unwrap removes the REST API's {"result": ..., "success": ...} envelope;
// the cf CLI prints the output bare.
func unwrap(body []byte) ([]byte, error) {
	var env struct {
		Result  json.RawMessage `json:"result"`
		Success *bool           `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &env) == nil && env.Success != nil {
		if !*env.Success || len(env.Result) == 0 {
			var msgs []string
			for _, e := range env.Errors {
				msgs = append(msgs, e.Message)
			}
			return nil, fmt.Errorf("Workers AI: %s", strings.Join(msgs, "; "))
		}
		return env.Result, nil
	}
	return body, nil
}

func parse(body []byte) (*Response, error) {
	body, err := unwrap(body)
	if err != nil {
		return nil, err
	}
	var r Response
	if err := json.Unmarshal(body, &r); err != nil || len(r.Answers) == 0 {
		return nil, fmt.Errorf("Clef 返回了无法识别的结果: %.200s", body)
	}
	return &r, nil
}

// LookCF finds the cf CLI on PATH or in npm global installs under nvm.
func LookCF() (string, error) {
	if p, err := exec.LookPath("cf"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, pattern := range []string{
		"/opt/homebrew/bin/cf", "/usr/local/bin/cf",
		filepath.Join(home, ".nvm/versions/node/*/bin/cf"),
		filepath.Join(home, ".local/share/pnpm/cf"),
		filepath.Join(home, ".bun/bin/cf"),
	} {
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			return m[len(m)-1], nil
		}
	}
	return "", errors.New("cf not found")
}

// CLIStatus describes the cf CLI on this device.
type CLIStatus struct {
	Found     bool   `json:"found"`
	Path      string `json:"path"`
	SignedIn  bool   `json:"signedIn"`
	Account   string `json:"account"`
	AccountID string `json:"accountId"`
}

// DetectCLI finds the cf CLI and checks that it is signed in.
func DetectCLI(ctx context.Context) CLIStatus {
	cf, err := LookCF()
	if err != nil {
		return CLIStatus{}
	}
	st := CLIStatus{Found: true, Path: cf}
	cmd := exec.CommandContext(ctx, cf, "auth", "whoami")
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(cf)+":"+os.Getenv("PATH")+":/opt/homebrew/bin:/usr/local/bin")
	out, err := cmd.Output()
	if err != nil {
		return st
	}
	var who struct {
		Authenticated bool `json:"authenticated"`
		Accounts      []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"accounts"`
	}
	if json.Unmarshal(out, &who) == nil && who.Authenticated {
		st.SignedIn = true
		if len(who.Accounts) > 0 {
			st.Account, st.AccountID = who.Accounts[0].Name, who.Accounts[0].ID
		}
	}
	return st
}
