package highlight

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
	"strings"
	"time"
)

// Model is the Workers AI model that judges clips. Clef-omni speaks the Jev
// decision API and, unlike text-only Jev, accepts video with its soundtrack.
const Model = "@cf/cloudflare/clef-omni"

// Verdict is the model's judgment of one clip.
type Verdict struct {
	// Keep is the probability the clip belongs in the highlight reel.
	Keep float64 `json:"keep"`
	// Score is the excitement level, 0 (boring) to 4 (epic).
	Score float64 `json:"score"`
	// Scene is the most likely kind of footage, one of SceneLabels.
	// Kill is the probability a champion is killed in the clip.
	Kill float64 `json:"kill"`
	// Opening is the probability the clip is the start of the game.
	Opening float64 `json:"opening"`
	// Ending is the probability the clip is the end of the game.
	Ending     float64            `json:"ending"`
	Scene      string             `json:"scene"`
	SceneProbs map[string]float64 `json:"sceneProbs"`
	Tokens     int                `json:"tokens"`
}

// QuestionsVersion changes whenever the questions do, so cached verdicts
// from older questions are judged again.
const QuestionsVersion = 2

// SceneLabels describes the kinds of footage in a broadcast.
var SceneLabels = map[string]string{
	"draft":     "champion select, bans and picks",
	"desk":      "casters, players, crowd, analysis desk, ads or a break",
	"laning":    "laning or farming with little action",
	"skirmish":  "small fights, ganks, single kills",
	"teamfight": "a large teamfight",
	"objective": "dragon, baron, herald, towers or inhibitor being taken",
	"replay":    "an instant replay of a play",
	"end":       "nexus destroyed, victory screen",
}

var scoreLevels = []string{
	"Boring: draft, desk, ads, idle farming",
	"Low: laning, minor trades",
	"Medium: skirmish or objective setup",
	"High: kills or an objective taken",
	"Epic: big teamfight, multikill, baron or elder, game-deciding",
}

func buildRequest(video []byte, dur float64) map[string]any {
	return map[string]any{
		"model": "clef-omni",
		"state": fmt.Sprintf("A %.0f-second clip from a League of Legends esports broadcast. "+
			"Decide whether it belongs in a short highlight reel of the match that cuts out boring parts.", dur),
		"videos": []string{"data:video/mp4;base64," + base64.StdEncoding.EncodeToString(video)},
		"questions": map[string]any{
			"keep": map[string]any{
				"type":         "noul",
				"instructions": "Should this clip be kept in the highlight reel?",
				"criteria": map[string]string{
					"true":  "Kills, teamfights, outplays, objectives taken, or the game ending",
					"false": "Draft, desk talk, ads, breaks, or quiet farming",
				},
			},
			"kill": map[string]any{
				"type":         "noul",
				"instructions": "Is at least one champion killed in this clip (a kill announcement, a kill in the kill feed, or a champion dying on screen)?",
				"criteria": map[string]string{
					"true":  "A champion dies or a kill is announced, including first blood, executes and shutdowns",
					"false": "No champion dies in this clip",
				},
			},
			"opening": map[string]any{
				"type":         "noul",
				"instructions": "Is this the opening of the game: the in-game match has just started (game timer near 0:00, champions leaving the fountain, walking to lanes, or the broadcast transitioning from the draft into the game)?",
				"criteria": map[string]string{
					"true":  "The first moments of the actual game",
					"false": "Draft, desk, or the game is already well underway",
				},
			},
			"ending": map[string]any{
				"type":         "noul",
				"instructions": "Is this the ending of the game: the final push, the nexus exploding, or the victory or defeat screen?",
				"criteria": map[string]string{
					"true":  "The game is being won or has just been won",
					"false": "The game is still going on, or this is a break or desk segment",
				},
			},
			"score": map[string]any{
				"type":         "score",
				"instructions": "How exciting is this clip for a highlight reel?",
				"criteria":     scoreLevels,
			},
			"scene": map[string]any{
				"type":         "choice",
				"instructions": "What does this clip mainly show?",
				"criteria":     SceneLabels,
			},
		},
	}
}

type answers struct {
	Answers struct {
		Keep    struct{ Noul float64 }  `json:"keep"`
		Kill    struct{ Noul float64 }  `json:"kill"`
		Opening struct{ Noul float64 }  `json:"opening"`
		Ending  struct{ Noul float64 }  `json:"ending"`
		Score   struct{ Score float64 } `json:"score"`
		Scene   struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"scene"`
	} `json:"answers"`
	Usage struct {
		InputTokens int `json:"input_tokens"`
	} `json:"usage"`
}

func parseVerdict(body []byte) (Verdict, error) {
	// The REST API wraps the model output in {"result": ..., "success": ...};
	// the cf CLI prints it bare.
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
			return Verdict{}, fmt.Errorf("clef-omni: %s", strings.Join(msgs, "; "))
		}
		body = env.Result
	}
	var a answers
	if err := json.Unmarshal(body, &a); err != nil {
		return Verdict{}, fmt.Errorf("clef-omni: unexpected response: %.200s", body)
	}
	if a.Answers.Scene.Choice == "" {
		return Verdict{}, fmt.Errorf("clef-omni: no answers in response: %.200s", body)
	}
	return Verdict{
		Keep:       a.Answers.Keep.Noul,
		Kill:       a.Answers.Kill.Noul,
		Opening:    a.Answers.Opening.Noul,
		Ending:     a.Answers.Ending.Noul,
		Score:      a.Answers.Score.Score,
		Scene:      a.Answers.Scene.Choice,
		SceneProbs: a.Answers.Scene.Probabilities,
		Tokens:     a.Usage.InputTokens,
	}, nil
}

// Judge asks the model about one clip.
type Judge interface {
	Judge(ctx context.Context, video []byte, dur float64) (Verdict, error)
	Name() string
}

// NewJudge uses the REST API when CLOUDFLARE_ACCOUNT_ID and
// CLOUDFLARE_API_TOKEN are set, and otherwise the signed-in `cf` CLI.
func NewJudge() (Judge, error) {
	account, token := os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_API_TOKEN")
	if account != "" && token != "" {
		return &restJudge{account: account, token: token, client: &http.Client{Timeout: 2 * time.Minute}}, nil
	}
	cf, err := lookCF()
	if err != nil {
		return nil, errors.New("no Cloudflare credentials: set CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN, or sign in with `cf auth login`")
	}
	return &cliJudge{cf: cf}, nil
}

type restJudge struct {
	account, token string
	client         *http.Client
}

func (j *restJudge) Name() string { return "Workers AI REST" }

func (j *restJudge) Judge(ctx context.Context, video []byte, dur float64) (Verdict, error) {
	payload, err := json.Marshal(buildRequest(video, dur))
	if err != nil {
		return Verdict{}, err
	}
	return withRetry(ctx, func() (Verdict, bool, error) {
		url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai/run/%s", j.account, Model)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return Verdict{}, false, err
		}
		req.Header.Set("Authorization", "Bearer "+j.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := j.client.Do(req)
		if err != nil {
			return Verdict{}, true, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return Verdict{}, true, fmt.Errorf("clef-omni: HTTP %d", resp.StatusCode)
		}
		v, err := parseVerdict(body)
		return v, false, err
	})
}

type cliJudge struct{ cf string }

func (j *cliJudge) Name() string { return "cf CLI" }

func (j *cliJudge) Judge(ctx context.Context, video []byte, dur float64) (Verdict, error) {
	f, err := os.CreateTemp("", "clef-*.json")
	if err != nil {
		return Verdict{}, err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(buildRequest(video, dur)); err != nil {
		f.Close()
		return Verdict{}, err
	}
	f.Close()
	return withRetry(ctx, func() (Verdict, bool, error) {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, j.cf, "ai", "run", Model, "--body", "@"+f.Name())
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return Verdict{}, true, fmt.Errorf("cf ai run: %w: %.300s", err, strings.TrimSpace(stderr.String()+stdout.String()))
		}
		v, err := parseVerdict(stdout.Bytes())
		return v, false, err
	})
}

// lookCF finds the cf CLI, including npm global installs under nvm.
func lookCF() (string, error) {
	if p, err := LookTool("cf"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/*/bin/cf"))
	if len(matches) > 0 {
		return matches[len(matches)-1], nil
	}
	return "", errors.New("cf not found")
}

func withRetry(ctx context.Context, try func() (Verdict, bool, error)) (Verdict, error) {
	var lastErr error
	for attempt := range 4 {
		v, retry, err := try()
		if err == nil {
			return v, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(time.Duration(1<<attempt) * 2 * time.Second):
		case <-ctx.Done():
			return Verdict{}, ctx.Err()
		}
	}
	return Verdict{}, lastErr
}
