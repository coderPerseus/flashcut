package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIToolCall(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"b"},{"id":"a"}]}`)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad", 400)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"content":"<think>x</think>好的","tool_calls":[{"id":"c1","function":{"name":"set_length","arguments":"{\"seconds\":60}"}}]}}]}`)
	}))
	defer srv.Close()
	p := Provider{Protocol: OpenAI, BaseURL: srv.URL + "/v1/", APIKey: "k"}
	c := New(nil)
	resp, err := c.Chat(context.Background(), p, "m", Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []Tool{{Name: "set_length", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "好的" || len(resp.ToolCalls) != 1 || string(resp.ToolCalls[0].Arguments) != `{"seconds":60}` {
		t.Fatalf("resp %+v", resp)
	}
	if got["model"] != "m" || got["tools"] == nil {
		t.Fatalf("request %v", got)
	}
	models, err := c.ListModels(context.Background(), p)
	if err != nil || strings.Join(models, ",") != "a,b" {
		t.Fatalf("models %v %v", models, err)
	}
}

func TestAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			http.Error(w, `{"error":{"message":"no"}}`, 401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["system"] != "be brief" {
			http.Error(w, "system missing", 400)
			return
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"你好"},{"type":"tool_use","id":"t1","name":"undo","input":{}}]}`)
	}))
	defer srv.Close()
	p := Provider{Protocol: Anthropic, BaseURL: srv.URL + "/v1", APIKey: "k"}
	resp, err := New(nil).Chat(context.Background(), p, "m", Request{Messages: []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}})
	if err != nil || resp.Text != "你好" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "undo" {
		t.Fatalf("resp %+v %v", resp, err)
	}
	p.APIKey = "wrong"
	if _, err := New(nil).Chat(context.Background(), p, "m", Request{Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil || !strings.Contains(err.Error(), "密钥") {
		t.Fatalf("want a key error, got %v", err)
	}
}
