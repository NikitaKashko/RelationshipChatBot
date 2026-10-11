package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeSearchGroundingPartialResultsAndSafeErrors(t *testing.T) {
	status := 200
	stop := "end_turn"
	sources := []any{map[string]any{"type": "web_search_result", "url": "https://example.com/cafe", "encrypted_content": "NEVER_FORWARD"}}
	rows := []any{
		map[string]any{"title": "Кафе", "categories": []string{"Покушать"}, "address": "Москва, улица 1", "city": "Москва", "price": 900, "price_basis": "person", "map_url": "https://yandex.ru/maps/org/invented/", "sources": []string{"https://example.com/cafe"}, "field_sources": map[string]string{"title": "https://example.com/cafe", "address": "https://example.com/cafe", "city": "https://example.com/cafe"}},
		map[string]any{"title": "Придуманное", "sources": []string{"https://invented.example/place"}, "field_sources": map[string]string{"title": "https://invented.example/place"}},
		map[string]any{"title": "Ложная ссылка на тот же источник", "sources": []string{"https://example.com/cafe"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "fixture-secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("bad native API headers")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil || !strings.Contains(string(body["tools"]), "web_search_20250305") {
			t.Error("no native search tool")
		}
		if strings.Contains(string(body["messages"]), "NEVER_FORWARD") {
			t.Error("encrypted content leaked")
		}
		w.WriteHeader(status)
		data, _ := json.Marshal(map[string]any{"summary": "Найден один вариант", "places": rows})
		json.NewEncoder(w).Encode(map[string]any{"stop_reason": stop, "content": []any{map[string]any{"type": "text", "text": "Сначала поищу подходящие места."}, map[string]any{"type": "web_search_tool_result", "content": sources}, map[string]any{"type": "web_search_tool_result", "content": map[string]string{"type": "web_search_tool_result_error", "error_code": "max_uses_exceeded"}}, map[string]any{"type": "text", "text": string(data)}}, "usage": map[string]any{"input_tokens": 100, "cache_read_input_tokens": 20, "output_tokens": 50, "server_tool_use": map[string]int{"web_search_requests": 2}}})
	}))
	defer server.Close()
	p := DeepSeekSearch{Key: "fixture-secret", Model: "deepseek-flash", Endpoint: server.URL, Client: server.Client()}
	found, err := p.Search(context.Background(), SearchRequest{Query: "найди новое кафе", City: "Москва", Mode: "discover"})
	if err != nil || len(found.Places) != 1 {
		t.Fatal("padding or lost partial results", err, len(found.Places))
	}
	c := found.Places[0].Card
	if c.Address == "" || c.Price != nil || c.MapURL != "" || len(c.Photos) != 0 || c.Sources[0].RetrievedAt <= 0 {
		t.Fatal("unproven facts accepted")
	}
	if found.Usage == nil || found.Usage.Input != 120 || found.Usage.Cached != 20 || found.Requests == nil || *found.Requests != 2 {
		t.Fatal("lost usage/attempt count")
	}
	rows = []any{}
	for _, title := range []string{"Один", "Два", "Три", "Четыре", "Пять", "Шесть"} {
		rows = append(rows, map[string]any{"title": title, "sources": []string{"https://example.com/cafe"}, "field_sources": map[string]string{"title": "https://example.com/cafe"}})
	}
	found, err = p.Search(context.Background(), SearchRequest{Mode: "discover"})
	if err != nil || len(found.Places) != 5 {
		t.Fatal("five supported cards not normalized", err)
	}
	found, err = p.Search(context.Background(), SearchRequest{Mode: "identify"})
	if err != nil || len(found.Places) != 3 {
		t.Fatal("ambiguous identity must give bounded alternatives", err)
	}
	stop = "max_tokens"
	found, err = p.Search(context.Background(), SearchRequest{})
	if err == nil || found.Usage == nil || len(found.Places) > 0 {
		t.Fatal("incomplete response accepted or usage lost")
	}
	status = 403
	_, err = p.Search(context.Background(), SearchRequest{})
	if err == nil || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), server.URL) {
		t.Fatal("unsafe search error")
	}
}

func TestSearchPermissionAndUnsafeLinks(t *testing.T) {
	for _, text := range []string{"куда сходить вечером", "Кафе Ладо на Кривоколенном", "добавь пикник у озера", "а подешевле?"} {
		if allow, _ := searchPermission(text); allow {
			t.Fatalf("implicit web request: %s", text)
		}
	}
	for _, text := range []string{"найди новые кафе", "поищи в интернете театр", "https://yandex.ru/maps/org/place/", "/search Кафе Ладо"} {
		if allow, _ := searchPermission(text); !allow {
			t.Fatalf("explicit search refused: %s", text)
		}
	}
	for _, text := range []string{"не ищи новые кафе", "найди новые места, но без интернета"} {
		if allow, refuse := searchPermission(text); allow || !refuse {
			t.Fatal("negative instruction ignored")
		}
	}
	for _, text := range []string{"https://127.0.0.1/place", "https://[::1]/place", "https://100.64.0.1/place", "https://metadata.google.internal/place", "http://example.com/place", "https://user:password@example.com/place"} {
		if safeSearchLinks(text) {
			t.Fatalf("unsafe link: %s", text)
		}
	}
	if !safeSearchLinks("Вот кафе https://yandex.ru/maps/org/cafe/.") {
		t.Fatal("public link refused")
	}
}
