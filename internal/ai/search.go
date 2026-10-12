package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

type SearchCard struct{ Preview, SaveToken, MapURL string }
type SearchButton struct{ Label, Token string }
type searchTurn struct {
	text, conversation string
	pair               int64
	mode               string
	attempted          bool
	value              any
}
type searchFailure struct{}

func (*searchFailure) Error() string {
	return "search failed; explicit retry required"
}

var links = regexp.MustCompile(`https?://[^\s<>"']+`)

func searchPermission(text string) (allow, refuse bool) {
	t := strings.ToLower(text)
	for _, no := range []string{"не ищи", "не искать", "не надо искать", "без интернета", "только из коллекции", "только коллекция"} {
		if strings.Contains(t, no) {
			return false, true
		}
	}
	if len(links.FindAllString(text, -1)) > 0 || strings.HasPrefix(t, "/search ") {
		return true, false
	}
	if strings.Contains(t, "хочу добавить место:") {
		return true, false
	}
	verb := false
	for _, v := range []string{"найди", "найти", "поищи", "поискать", "ищи", "ищем", "давай поиск"} {
		if strings.Contains(t, v) {
			verb = true
		}
	}
	for _, marker := range []string{"в интернете", "в сети", "новые", "новое", "новую", "новый", "сайт"} {
		if verb && strings.Contains(t, marker) {
			return true, false
		}
	}
	return false, false
}
func safeSearchLinks(text string) bool {
	for _, raw := range links.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ").,;!?»")
		if !collection.ValidURL(raw) {
			return false
		}
		u, _ := url.Parse(raw)
		h := strings.ToLower(u.Hostname())
		for _, suffix := range []string{".internal", ".lan", ".test", ".invalid"} {
			if strings.HasSuffix(h, suffix) {
				return false
			}
		}
		if ip := net.ParseIP(h); ip != nil && !collection.PublicIP(ip) {
			return false
		}
	}
	return true
}
func (s *Service) directSearch(ctx context.Context, actor int64, turn *searchTurn, result Result) (Result, error) {
	mode := "discover"
	if len(links.FindAllString(turn.text, -1)) > 0 || strings.Contains(strings.ToLower(turn.text), "хочу добавить место:") {
		mode = "identify"
	}
	args, _ := json.Marshal(map[string]string{"mode": mode})
	value, err := s.searchTool(ctx, actor, string(args), turn, &result)
	var failed *searchFailure
	if errors.As(err, &failed) {
		result.Retry, err = s.retry(ctx, actor, turn.pair, turn.conversation, turn.text)
		result.Text = texts.T("internal.ai.search.022")
		return result, err
	}
	if err != nil {
		return result, err
	}
	result.Text = texts.T("internal.ai.search.023")
	if data, ok := value.(map[string]any); ok {
		if len(result.SearchCards) > 0 {
			result.Text = searchResultTitle(mode, len(result.SearchCards))
		} else if summary, ok := data["summary"].(string); ok && summary != "" {
			result.Text = summary
		}
	} else if data, ok := value.(map[string]string); ok {
		switch data["status"] {
		case "internet_forbidden_by_user":
			result.Text = texts.T("internal.ai.search.024")
		case "search_not_configured":
			result.Text = texts.T("internal.ai.search.025")
		case "unsafe_link", "city_needed":
			result.Text = data["message"]
		}
	}
	if err = s.current(ctx, actor, turn.pair, turn.conversation); err != nil {
		return Result{}, err
	}
	err = s.Store.Write(ctx, func(q store.Query) error {
		_, err := q.ExecContext(ctx, "INSERT INTO messages(conversation_id,role,text,created_at) VALUES(?,'assistant',?,?)", turn.conversation, result.Text, s.Store.Now().Unix())
		return err
	})
	return result, err
}

// One AI consent covers provider use. The current message still controls whether
// discover mode may use the network; identify mode represents a concrete place
// link/description selected by the model. Retrieved pages never grant permission.
func (s *Service) searchTool(ctx context.Context, actor int64, args string, turn *searchTurn, result *Result) (any, error) {
	var p struct {
		City string `json:"city"`
		Mode string `json:"mode"`
	}
	if err := decode(args, &p); err != nil {
		return nil, err
	}
	if p.Mode != "discover" && p.Mode != "identify" {
		return nil, store.ErrDenied
	}
	turn.mode = p.Mode
	allow, refuse := searchPermission(turn.text)
	if refuse {
		return map[string]string{"status": "internet_forbidden_by_user"}, nil
	}
	if s.Search == nil {
		return map[string]string{"status": "search_not_configured"}, nil
	}
	if !allow && p.Mode == "discover" {
		return map[string]string{"status": "internet_not_requested", "message": texts.T("internal.ai.search.026")}, nil
	}
	if !safeSearchLinks(turn.text) {
		return map[string]string{"status": "unsafe_link", "message": texts.T("internal.ai.search.027")}, nil
	}
	if p.City == "" {
		data, err := s.readContext(ctx, actor)
		if errors.Is(err, store.ErrDenied) {
			user, e := store.UserFor(ctx, s.Store.DB, actor, false)
			if e != nil {
				return nil, e
			}
			p.City = user.City
		} else if err != nil {
			return nil, err
		} else {
			p.City, _ = data.(map[string]any)["city"].(string)
		}
	}
	if p.City == "" && len(links.FindAllString(turn.text, -1)) == 0 {
		return map[string]string{"status": "city_needed", "message": texts.T("internal.ai.search.028")}, nil
	}
	if turn.attempted {
		return turn.value, nil
	} // No automatic paid re-search within a turn.
	if err := s.current(ctx, actor, turn.pair, turn.conversation); err != nil {
		return nil, err
	}
	turn.attempted = true
	var usageID int64
	err := s.Store.Write(ctx, func(q store.Query) error {
		r, e := q.ExecContext(ctx, "INSERT INTO api_usage(user_id,provider,model,uncertain,success,created_at) VALUES(?,'deepseek_search',?,1,0,?)", actor, s.Model, s.Store.Now().Unix())
		if e != nil {
			return e
		}
		usageID, e = r.LastInsertId()
		return e
	})
	if err != nil {
		return nil, err
	}
	found, e := s.Search.Search(ctx, SearchRequest{Query: turn.text, City: p.City, Mode: p.Mode})
	accountCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = s.account(accountCtx, usageID, found.Usage, e == nil)
	if err == nil {
		err = s.Store.Write(accountCtx, func(q store.Query) error {
			_, err := q.ExecContext(accountCtx, "UPDATE api_usage SET search_requests=?,uncertain=1 WHERE id=?", found.Requests, usageID)
			return err
		})
	}
	cancel()
	if err != nil {
		return nil, err
	}
	if e != nil {
		return nil, &searchFailure{}
	}
	if err = s.current(ctx, actor, turn.pair, turn.conversation); err != nil {
		return nil, err
	}
	prepared := []SearchCard{}
	metadata := []map[string]any{}
	excluded := 0
	for _, place := range found.Places {
		limit := 5
		if p.Mode == "identify" {
			limit = 3
		}
		if len(prepared)+len(result.Places) >= limit {
			break
		}
		card := place.Card
		if len(card.Sources) == 0 || len(card.Photos) > 0 {
			continue
		}
		for i := range card.Sources {
			card.Sources[i].RetrievedAt = s.Store.Now().Unix()
		}
		if collection.Validate(card) != nil {
			continue
		}
		cards := collection.Service{Store: s.Store}
		match, err := cards.Match(ctx, s.Store.DB, actor, card)
		if err != nil {
			return nil, err
		}
		if match != nil && turn.pair != 0 {
			if p.Mode == "discover" && !collection.AutomaticEligible(*match, float64(s.Store.Now().Unix())) {
				excluded++
				continue
			}
			result.Places = append(result.Places, match.ID)
			metadata = append(metadata, map[string]any{"already_in_collection": true, "id": match.ID, "title": match.Title, "frozen_until": match.FreezeUntil, "refused": match.Refused, "archived": match.Archived})
			continue
		}
		mapURL := card.MapURL
		if mapURL == "" {
			mapURL = "https://yandex.ru/maps/?text=" + url.QueryEscape(strings.TrimSpace(card.Title+" "+card.City+" "+card.Address))
		}
		preview := renderSearchPreview(card, place.Why, mapURL)
		card.Description = strings.TrimSpace(card.Description + texts.T("internal.ai.search.029"))
		// Persist the caution and relevance together with the reviewed card, so
		// a saved incomplete card never turns into an apparently verified record.
		if place.Why != "" {
			card.Description += texts.T("internal.ai.search.030") + limitText(place.Why, 250)
		}
		svc := actions.Service{Store: s.Store}
		raw, _ := json.Marshal(collection.SearchSave{Card: card})
		idea, err := svc.Prepare(ctx, actor, "ai:search_save", raw)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, SearchCard{Preview: preview, SaveToken: idea, MapURL: mapURL})
		metadata = append(metadata, map[string]any{"title": card.Title, "address": card.Address, "city": card.City, "price": card.Price, "price_basis": card.PriceBasis, "unknown": place.Unknown})
	}
	result.SearchCards = prepared
	summary := found.Summary
	if excluded > 0 {
		summary = texts.T("internal.ai.search.031")
	}
	turn.value = map[string]any{"found": metadata, "summary": summary, "saved": false, "cards_prepared": len(prepared), "excluded": excluded, "instruction": texts.T("internal.ai.search.032")}
	if len(prepared) > 0 || len(result.Places) > 0 || excluded > 0 {
		titles := []string{}
		for _, p := range found.Places {
			titles = append(titles, p.Card.Title)
		}
		token, err := s.retry(ctx, actor, turn.pair, turn.conversation, "/search "+turn.text+texts.T("internal.ai.search.033")+strings.Join(titles, ", "))
		if err != nil {
			return nil, err
		}
		result.SearchButton = &SearchButton{Label: texts.T("internal.ai.search.034"), Token: token}
	}
	return turn.value, nil
}
func searchResultTitle(mode string, count int) string {
	if mode == "identify" {
		if count == 1 {
			return texts.T("internal.ai.search.035")
		}
		return fmt.Sprintf(texts.T("internal.ai.search.036"), count)
	}
	return fmt.Sprintf(texts.T("internal.ai.search.037"), count)
}
func renderSearchPreview(card collection.Input, why, mapURL string) string {
	lines := []string{"📍 " + card.Title}
	if len(card.Categories) > 0 {
		lines = append(lines, texts.T("internal.ai.search.038")+strings.Join(card.Categories, ", "))
	}
	if card.Address != "" {
		lines = append(lines, texts.T("internal.ai.search.039")+card.Address)
	}
	if card.Description != "" {
		lines = append(lines, card.Description)
	}
	if card.Price != nil {
		basis := texts.T("internal.ai.search.040")
		if card.PriceBasis == "couple" {
			basis = texts.T("internal.ai.search.041")
		}
		lines = append(lines, texts.T("internal.ai.search.042")+strconv.FormatFloat(*card.Price, 'f', -1, 64)+" ₽ "+basis)
	}
	if why != "" {
		lines = append(lines, texts.T("internal.ai.search.043")+limitText(why, 250))
	}
	if mapURL != "" {
		lines = append(lines, texts.T("internal.ai.search.044")+mapURL)
	}
	for i, source := range card.Sources {
		if i >= 2 {
			break
		}
		lines = append(lines, texts.T("internal.ai.search.045")+source.URL)
	}
	return strings.Join(lines, "\n")
}
