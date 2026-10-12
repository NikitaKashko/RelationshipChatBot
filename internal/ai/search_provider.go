package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

type SearchRequest struct {
	Query string `json:"query"`
	City  string `json:"city"`
	Mode  string `json:"mode"` // discover or identify; never permission to search.
}
type FoundPlace struct {
	Card    collection.Input `json:"card"`
	Why     string           `json:"why"`
	Unknown []string         `json:"unknown"`
}
type SearchCompletion struct {
	Places   []FoundPlace
	Summary  string
	Usage    *Usage
	Requests *int64
}
type SearchProvider interface {
	Search(context.Context, SearchRequest) (SearchCompletion, error)
}
type DeepSeekSearch struct {
	Key, Model, Endpoint string
	Client               *http.Client
}

var searchInstruction = texts.T("internal.ai.search_provider.001")

func (p *DeepSeekSearch) Search(ctx context.Context, request SearchRequest) (result SearchCompletion, err error) {
	if p.Key == "" {
		return result, errors.New("search not configured")
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/anthropic/v1/messages"
	}
	query, _ := json.Marshal(request)
	body, _ := json.Marshal(map[string]any{"model": p.Model, "max_tokens": 4500, "thinking": map[string]string{"type": "disabled"}, "system": searchInstruction, "messages": []any{map[string]any{"role": "user", "content": string(query)}}, "tools": []any{map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 5}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("invalid search endpoint")
	}
	req.Header.Set("x-api-key", p.Key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 75 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	response, err := client.Do(req)
	if err != nil {
		return result, errors.New("search request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("search provider rejected request")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return result, errors.New("invalid search response")
	}
	var envelope struct {
		Stop    string `json:"stop_reason"`
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
		Usage *struct {
			Input    *int64 `json:"input_tokens"`
			Cached   int64  `json:"cache_read_input_tokens"`
			Creation int64  `json:"cache_creation_input_tokens"`
			Output   *int64 `json:"output_tokens"`
			Server   *struct {
				Requests *int64 `json:"web_search_requests"`
			} `json:"server_tool_use"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return result, errors.New("invalid search response")
	}
	if envelope.Usage != nil {
		u := envelope.Usage
		// Anthropic input excludes cache reads/writes; sum conservatively. Native
		// search may incur additional summary tokens not exposed in this response.
		if u.Input != nil && u.Output != nil && u.Cached >= 0 && u.Creation >= 0 {
			result.Usage = &Usage{Input: *u.Input + u.Cached + u.Creation, Cached: u.Cached, Output: *u.Output}
		}
		if u.Server != nil {
			result.Requests = u.Server.Requests
		}
	}
	if envelope.Stop != "end_turn" {
		return result, errors.New("search answer incomplete")
	}
	allowed := map[string]bool{}
	var answerTexts []string
	for _, block := range envelope.Content {
		if block.Type == "text" {
			answerTexts = append(answerTexts, block.Text)
		}
		if block.Type != "web_search_tool_result" {
			continue
		}
		var hits []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}
		if json.Unmarshal(block.Content, &hits) != nil {
			continue
		} // Tool errors are objects.
		for _, hit := range hits {
			if hit.Type == "web_search_result" && collection.ValidURL(hit.URL) {
				allowed[hit.URL] = true
			}
		}
	}
	// Native search can emit a short preamble before server_tool_use. The final
	// text block, rather than concatenated preamble+answer, contains the JSON.
	answer := ""
	if len(answerTexts) > 0 {
		answer = strings.TrimSpace(answerTexts[len(answerTexts)-1])
	}
	if strings.HasPrefix(answer, "```") {
		_, answer, _ = strings.Cut(answer, "\n")
		answer = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(answer), "```"))
	}
	if a, b := strings.Index(answer, "{"), strings.LastIndex(answer, "}"); a >= 0 && b > a {
		answer = answer[a : b+1]
	}
	var data struct {
		Summary string `json:"summary"`
		Places  []struct {
			Title       string            `json:"title"`
			Categories  []string          `json:"categories"`
			Address     string            `json:"address"`
			City        string            `json:"city"`
			MapURL      string            `json:"map_url"`
			Description string            `json:"description"`
			Price       *float64          `json:"price"`
			Basis       string            `json:"price_basis"`
			Why         string            `json:"why"`
			Sources     []string          `json:"sources"`
			Fields      map[string]string `json:"field_sources"`
		} `json:"places"`
	}
	if decode(answer, &data) != nil {
		return result, errors.New("search returned no structured cards")
	}
	result.Summary = limitText(data.Summary, 800)
	seen := map[string]bool{}
	for _, row := range data.Places {
		if len(result.Places) >= 5 || request.Mode == "identify" && len(result.Places) >= 3 {
			break
		}
		p := collection.Input{Title: strings.TrimSpace(row.Title), Categories: row.Categories}
		own := map[string]bool{}
		for _, source := range row.Sources {
			if allowed[source] && !own[source] && len(p.Sources) < 3 {
				own[source] = true
				p.Sources = append(p.Sources, collection.Source{URL: source})
			}
		}
		if len(p.Sources) == 0 || p.Title == "" || len([]rune(p.Title)) > 200 {
			continue
		}
		proven := func(field string) bool {
			return own[row.Fields[field]]
		}
		if !proven("title") {
			continue
		}
		if proven("address") {
			p.Address = limitText(row.Address, 300)
		}
		if proven("city") {
			p.City = limitText(row.City, 120)
		}
		if proven("description") {
			p.Description = limitText(row.Description, 500)
		}
		if proven("price") && row.Price != nil {
			p.Price = row.Price
			p.PriceBasis = row.Basis
		}
		if own[row.MapURL] && yandexMap(row.MapURL) {
			p.MapURL = row.MapURL
		}
		key := strings.ToLower(p.Title + "|" + p.Address + "|" + p.City)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(p.Categories) == 0 {
			p.Categories = []string{texts.T("internal.ai.search_provider.002")}
		}
		// Stamp actual retrieval on the server, never trust model-generated dates.
		for i := range p.Sources {
			p.Sources[i].RetrievedAt = time.Now().Unix()
		}
		if collection.Validate(p) != nil {
			continue
		}
		unknown := []string{texts.T("internal.ai.search_provider.003"), texts.T("internal.ai.search_provider.004")}
		if p.Address == "" {
			unknown = append(unknown, texts.T("internal.ai.search_provider.005"))
		}
		if p.Price == nil {
			unknown = append(unknown, texts.T("internal.ai.search_provider.006"))
		}
		if p.MapURL == "" {
			unknown = append(unknown, texts.T("internal.ai.search_provider.007"))
		}
		result.Places = append(result.Places, FoundPlace{Card: p, Why: limitText(row.Why, 250), Unknown: unknown})
	}
	if len(result.Places) == 0 && result.Summary == "" {
		result.Summary = texts.T("internal.ai.search_provider.008")
	}
	return result, nil
}
func limitText(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}
func yandexMap(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || !collection.ValidURL(raw) {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "yandex.ru", "yandex.com", "yandex.by", "yandex.kz", "maps.yandex.ru":
		return strings.HasPrefix(u.Path, "/maps") || u.Hostname() == "maps.yandex.ru"
	}
	return false
}
