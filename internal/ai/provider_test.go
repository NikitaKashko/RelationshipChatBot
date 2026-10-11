package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekHTTPContractAndSafeErrors(t *testing.T) {
	status := 200
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("missing credential header")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body["messages"]), "fixture-secret") {
			t.Error("secret in model context")
		}
		if string(body["thinking"]) != `{"type":"disabled"}` {
			t.Error("thinking unexpectedly enabled")
		}
		w.WriteHeader(status)
		if status != 200 {
			w.Write([]byte("PRIVATE_PROVIDER_ERROR_fixture-secret"))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_collection","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`))
	}))
	defer server.Close()
	p := DeepSeek{Key: "fixture-secret", Model: "deepseek-flash", Endpoint: server.URL}
	out, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}}, tools())
	if err != nil || out.Usage == nil || out.Usage.Cached != 2 || len(out.Message.Calls) != 1 {
		t.Fatal("API tool/usage contract", out, err)
	}
	status = 500
	_, err = p.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}}, tools())
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("unsafe provider error", err)
	}
	if requests != 2 {
		t.Fatal("HTTP automatic retry happened")
	}
}
