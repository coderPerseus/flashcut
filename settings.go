package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"flashcut/internal/clef"
	"flashcut/internal/llm"
	"flashcut/internal/media"
)

// ModelRef picks a model on a provider.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type storedSettings struct {
	Cloudflare      clef.Config    `json:"cloudflare"`
	Providers       []llm.Provider `json:"providers"`
	Chat            ModelRef       `json:"chat"`
	Fixer           ModelRef       `json:"fixer"`
	Vision          ModelRef       `json:"vision"`
	Concurrency     int            `json:"concurrency"`
	Quality         media.Quality  `json:"quality"`
	DefaultTemplate string         `json:"defaultTemplate"`

	// Before model settings existed the Cloudflare fields lived at the top.
	LegacyAccountID string `json:"accountId,omitempty"`
	LegacyAPIToken  string `json:"apiToken,omitempty"`
}

// cloudflareProvider is the built-in entry for Workers AI chat models. It
// uses the app's Cloudflare connection, so it has no key of its own.
var cloudflareProvider = llm.Provider{
	ID: "cloudflare", Name: "Cloudflare Workers AI", Protocol: llm.WorkersAI, Builtin: true, Models: llm.WorkersAIModels,
}

var settingsMu sync.Mutex

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func loadSettings() storedSettings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := storedSettings{Concurrency: 4, Quality: media.QualityMedium}
	if data, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Cloudflare.AccountID == "" && s.LegacyAccountID != "" {
		s.Cloudflare = clef.Config{Mode: "token", AccountID: s.LegacyAccountID, APIToken: s.LegacyAPIToken}
	}
	if s.Concurrency < 1 || s.Concurrency > 8 {
		s.Concurrency = 4
	}
	if s.Quality == "" {
		s.Quality = media.QualityMedium
	}
	if s.Chat.Model == "" {
		s.Chat = ModelRef{Provider: "cloudflare", Model: "@cf/zai-org/glm-4.7-flash"}
	}
	if s.Fixer.Model == "" {
		s.Fixer = ModelRef{Provider: "cloudflare", Model: "@cf/qwen/qwen3-30b-a3b-fp8"}
	}
	if s.Vision.Model == "" {
		s.Vision = ModelRef{Provider: "cloudflare", Model: "@cf/qwen/qwen3.8-27b"}
	}
	return s
}

func saveSettings(s storedSettings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s.LegacyAccountID, s.LegacyAPIToken = "", ""
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	tmp := settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath())
}

func (s storedSettings) provider(id string) (llm.Provider, bool) {
	if id == cloudflareProvider.ID {
		return cloudflareProvider, true
	}
	for _, p := range s.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return llm.Provider{}, false
}

func newClient() (*clef.Client, int, error) {
	st := loadSettings()
	c, err := clef.New(st.Cloudflare)
	return c, st.Concurrency, err
}

// chatClient returns an LLM client and the provider for ref.
func chatClient(ref ModelRef) (*llm.Client, llm.Provider, error) {
	st := loadSettings()
	p, ok := st.provider(ref.Provider)
	if !ok {
		return nil, p, fmt.Errorf("找不到模型服务 %q，请在模型设置里重新选择", ref.Provider)
	}
	var cf *clef.Client
	if p.Protocol == llm.WorkersAI {
		c, err := clef.New(st.Cloudflare)
		if err != nil {
			return nil, p, err
		}
		cf = c
	}
	return llm.New(cf), p, nil
}

// ---- general preferences ----

// Settings are the general preferences.
type Settings struct {
	Concurrency     int           `json:"concurrency"`
	Quality         media.Quality `json:"quality"`
	DefaultTemplate string        `json:"defaultTemplate"`
	// FFmpeg is empty when ffmpeg is installed, else what is wrong.
	FFmpeg string `json:"ffmpeg"`
}

// GetSettings returns the general preferences.
func (s *Studio) GetSettings() Settings {
	st := loadSettings()
	out := Settings{Concurrency: st.Concurrency, Quality: st.Quality, DefaultTemplate: st.DefaultTemplate}
	if _, err := media.LookTool("ffmpeg"); err != nil {
		out.FFmpeg = err.Error()
	}
	return out
}

// SaveSettings stores the general preferences.
func (s *Studio) SaveSettings(concurrency int, quality media.Quality, defaultTemplate string) (Settings, error) {
	st := loadSettings()
	st.Concurrency, st.Quality, st.DefaultTemplate = concurrency, quality, defaultTemplate
	if err := saveSettings(st); err != nil {
		return Settings{}, err
	}
	return s.GetSettings(), nil
}

// ---- model settings ----

// ProviderView is a provider as the page sees it: the key is masked.
type ProviderView struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	BaseURL  string   `json:"baseUrl"`
	HasKey   bool     `json:"hasKey"`
	KeyHint  string   `json:"keyHint"`
	Models   []string `json:"models"`
	Builtin  bool     `json:"builtin"`
}

// CloudflareView is the Clef connection as the page sees it.
type CloudflareView struct {
	Mode      string         `json:"mode"`
	AccountID string         `json:"accountId"`
	HasToken  bool           `json:"hasToken"`
	KeyHint   string         `json:"keyHint"`
	CLI       clef.CLIStatus `json:"cli"`
	// Using says how calls are made now ("cf CLI" or "API Token"), or why
	// they cannot be.
	Using     string `json:"using"`
	Connected bool   `json:"connected"`
}

// ModelSettings is the model configuration page.
type ModelSettings struct {
	Cloudflare CloudflareView `json:"cloudflare"`
	Providers  []ProviderView `json:"providers"`
	Chat       ModelRef       `json:"chat"`
	Fixer      ModelRef       `json:"fixer"`
	Vision     ModelRef       `json:"vision"`
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "••••"
	}
	return k[:3] + "…" + k[len(k)-4:]
}

func viewProvider(p llm.Provider) ProviderView {
	return ProviderView{ID: p.ID, Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL, HasKey: p.APIKey != "", KeyHint: maskKey(p.APIKey), Models: p.Models, Builtin: p.Builtin}
}

var (
	cliMu     sync.Mutex
	cliCache  clef.CLIStatus
	cliCached time.Time
)

func detectCLI(ctx context.Context, fresh bool) clef.CLIStatus {
	cliMu.Lock()
	defer cliMu.Unlock()
	if fresh || time.Since(cliCached) > time.Minute {
		cliCache, cliCached = clef.DetectCLI(ctx), time.Now()
	}
	return cliCache
}

// GetModelSettings returns the model configuration. It checks whether the
// cf CLI is installed and signed in.
func (s *Studio) GetModelSettings(ctx context.Context) ModelSettings {
	st := loadSettings()
	cf := CloudflareView{
		Mode: st.Cloudflare.Mode, AccountID: st.Cloudflare.AccountID, HasToken: st.Cloudflare.APIToken != "",
		KeyHint: maskKey(st.Cloudflare.APIToken), CLI: detectCLI(ctx, false),
	}
	if cf.Mode == "" {
		cf.Mode = "auto"
	}
	if c, err := clef.New(st.Cloudflare); err != nil {
		cf.Using = err.Error()
	} else {
		cf.Using, cf.Connected = c.Name(), true
		if c.Name() == "cf CLI" && !cf.CLI.SignedIn {
			cf.Using, cf.Connected = "找到了 cf 命令行，但还没有登录：在终端运行 cf auth login，或改用 API Token", false
		}
	}
	out := ModelSettings{Cloudflare: cf, Chat: st.Chat, Fixer: st.Fixer, Vision: st.Vision}
	out.Providers = append(out.Providers, viewProvider(cloudflareProvider))
	for _, p := range st.Providers {
		out.Providers = append(out.Providers, viewProvider(p))
	}
	return out
}

// SaveCloudflare sets how Clef is reached: mode "auto", "cli" or "token".
// An empty token keeps the saved one; clear removes the saved token.
func (s *Studio) SaveCloudflare(ctx context.Context, mode, accountID, token string, clear bool) (ModelSettings, error) {
	st := loadSettings()
	st.Cloudflare.Mode = mode
	st.Cloudflare.AccountID = strings.TrimSpace(accountID)
	if t := strings.TrimSpace(token); t != "" {
		st.Cloudflare.APIToken = t
	}
	if clear {
		st.Cloudflare.APIToken = ""
	}
	if err := saveSettings(st); err != nil {
		return ModelSettings{}, err
	}
	detectCLI(ctx, true)
	return s.GetModelSettings(ctx), nil
}

// ProviderInput edits a provider. An empty APIKey keeps the saved key.
type ProviderInput struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	BaseURL  string   `json:"baseUrl"`
	APIKey   string   `json:"apiKey"`
	Models   []string `json:"models"`
}

func (in ProviderInput) merge(st storedSettings) (llm.Provider, error) {
	if in.ID == cloudflareProvider.ID {
		return llm.Provider{}, errors.New("Cloudflare 的连接在上面单独设置")
	}
	p := llm.Provider{ID: in.ID, Name: strings.TrimSpace(in.Name), Protocol: in.Protocol, BaseURL: strings.TrimSpace(in.BaseURL), APIKey: strings.TrimSpace(in.APIKey), Models: in.Models}
	if old, ok := st.provider(in.ID); ok && p.APIKey == "" {
		p.APIKey = old.APIKey
	}
	if p.ID == "" {
		p.ID = fmt.Sprintf("p%d", time.Now().UnixNano()%1e10)
	}
	if p.Name == "" {
		p.Name = p.BaseURL
	}
	if !slices.Contains([]string{llm.OpenAI, llm.Anthropic}, p.Protocol) {
		return p, errors.New("协议只能是 OpenAI 兼容或 Anthropic")
	}
	if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return p, errors.New("Base URL 需要以 http:// 或 https:// 开头，例如 https://api.openai.com/v1")
	}
	return p, nil
}

// SaveProvider adds or updates a model service.
func (s *Studio) SaveProvider(ctx context.Context, in ProviderInput) (ModelSettings, error) {
	st := loadSettings()
	p, err := in.merge(st)
	if err != nil {
		return ModelSettings{}, err
	}
	i := slices.IndexFunc(st.Providers, func(x llm.Provider) bool { return x.ID == p.ID })
	if i >= 0 {
		st.Providers[i] = p
	} else {
		st.Providers = append(st.Providers, p)
	}
	if err := saveSettings(st); err != nil {
		return ModelSettings{}, err
	}
	return s.GetModelSettings(ctx), nil
}

// DeleteProvider removes a model service; roles using it fall back to
// Cloudflare.
func (s *Studio) DeleteProvider(ctx context.Context, id string) (ModelSettings, error) {
	st := loadSettings()
	st.Providers = slices.DeleteFunc(st.Providers, func(p llm.Provider) bool { return p.ID == id })
	for _, ref := range []*ModelRef{&st.Chat, &st.Fixer, &st.Vision} {
		if ref.Provider == id {
			*ref = ModelRef{}
		}
	}
	if err := saveSettings(st); err != nil {
		return ModelSettings{}, err
	}
	return s.GetModelSettings(ctx), nil
}

// FetchModels lists the models a service offers, using the form's values
// (with the saved key when the form leaves it empty).
func (s *Studio) FetchModels(ctx context.Context, in ProviderInput) ([]string, error) {
	if in.ID == cloudflareProvider.ID {
		return llm.WorkersAIModels, nil
	}
	p, err := in.merge(loadSettings())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return llm.New(nil).ListModels(ctx, p)
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK        bool   `json:"ok"`
	Reply     string `json:"reply"`
	Error     string `json:"error"`
	LatencyMs int64  `json:"latencyMs"`
	Model     string `json:"model"`
}

// TestModel sends a short greeting to a model and returns its reply.
func (s *Studio) TestModel(ctx context.Context, in ProviderInput, model string) TestResult {
	st := loadSettings()
	var p llm.Provider
	if in.ID == cloudflareProvider.ID {
		p = cloudflareProvider
	} else {
		var err error
		if p, err = in.merge(st); err != nil {
			return TestResult{Error: err.Error()}
		}
	}
	var cf *clef.Client
	if p.Protocol == llm.WorkersAI {
		c, err := clef.New(st.Cloudflare)
		if err != nil {
			return TestResult{Error: err.Error()}
		}
		cf = c
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := llm.New(cf).Chat(ctx, p, model, llm.Request{MaxTokens: 120, Messages: []llm.Message{
		{Role: "user", Content: "你好！请用一句话介绍你自己。"},
	}})
	if err != nil {
		return TestResult{Error: err.Error(), Model: model}
	}
	return TestResult{OK: true, Reply: resp.Text, LatencyMs: resp.Latency.Milliseconds(), Model: model}
}

// TestCloudflare asks Clef-flash a yes/no question to check the connection.
func (s *Studio) TestCloudflare(ctx context.Context) TestResult {
	c, _, err := newClient()
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := c.Run(ctx, clef.Flash, clef.Request{
		State:     "用户说：你好！",
		Questions: map[string]clef.Question{"greeting": clef.Noul("Is the user greeting someone?", "", "")},
	})
	if err != nil {
		return TestResult{Error: err.Error(), Model: clef.Flash}
	}
	return TestResult{
		OK: true, Model: clef.Flash, LatencyMs: time.Since(start).Milliseconds(),
		Reply: fmt.Sprintf("通过 %s 连接成功。Clef-flash 判断“你好！”是问候的概率为 %.0f%%。", c.Name(), resp.Answers["greeting"].Noul*100),
	}
}

// SetRoles chooses the models for chat, subtitle fixing and screen reading.
func (s *Studio) SetRoles(ctx context.Context, chat, fixer, vision ModelRef) (ModelSettings, error) {
	st := loadSettings()
	st.Chat, st.Fixer, st.Vision = chat, fixer, vision
	if err := saveSettings(st); err != nil {
		return ModelSettings{}, err
	}
	return s.GetModelSettings(ctx), nil
}

// ChatModelName says which model answers chat, for the chat header.
func (s *Studio) ChatModelName() string {
	st := loadSettings()
	if st.Chat.Model == "" {
		return "Clef-flash（只理解剪辑指令）"
	}
	p, ok := st.provider(st.Chat.Provider)
	if !ok {
		return st.Chat.Model + "（服务已删除）"
	}
	return st.Chat.Model + " · " + p.Name
}
