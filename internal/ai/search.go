package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type SearchCard struct{ Preview, Ideas, Candidates string }
type SearchButton struct{ Label, Token string }
type searchTurn struct {
	text, conversation string
	pair               int64
	attempted          bool
	value              any
}
type searchFailure struct{}

func (*searchFailure) Error() string { return "search failed; explicit retry required" }

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
	if len(links.FindAllString(turn.text, -1)) > 0 || strings.Contains(turn.text, "Хочу добавить место:") {
		mode = "identify"
	}
	args, _ := json.Marshal(map[string]string{"mode": mode})
	value, err := s.searchTool(ctx, actor, string(args), turn, &result)
	var failed *searchFailure
	if errors.As(err, &failed) {
		result.Retry, err = s.retry(ctx, actor, turn.pair, turn.conversation, turn.text)
		result.Text = "Поиск сейчас не ответил. Запрос сохранён; повторить можно по кнопке."
		return result, err
	}
	if err != nil {
		return result, err
	}
	result.Text = "Не удалось найти заполненную карточку. Уточни название, город или адрес."
	if data, ok := value.(map[string]any); ok {
		if summary, ok := data["summary"].(string); ok && summary != "" {
			result.Text = "ИИ по результатам поиска: " + summary
		}
		if len(result.SearchCards) > 0 {
			result.Text += "\nПроверь карточки ниже. Пока ничего не сохранено."
		}
	} else if data, ok := value.(map[string]string); ok {
		switch data["status"] {
		case "internet_forbidden_by_user":
			result.Text = "Интернет-поиск не запускаю. Можем выбрать из копилки."
		case "search_not_configured":
			result.Text = "Поиск ещё не настроен. Можно добавить идею вручную через /add название | категория."
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

// The model can request a search, but only the current human message or a
// single-use human button authorizes a network call. Old chat/source text cannot.
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
	allow, refuse := searchPermission(turn.text)
	if refuse {
		return map[string]string{"status": "internet_forbidden_by_user"}, nil
	}
	if s.Search == nil {
		return map[string]string{"status": "search_not_configured"}, nil
	}
	if !allow {
		if result.SearchButton == nil {
			token, err := s.retry(ctx, actor, turn.pair, turn.conversation, "/search "+turn.text)
			if err != nil {
				return nil, err
			}
			result.SearchButton = &SearchButton{Label: "Найти место в интернете", Token: token}
		}
		return map[string]string{"status": "explicit_permission_needed", "message": "Предложена кнопка поиска; ничего не найдено и не сохранено."}, nil
	}
	if !safeSearchLinks(turn.text) {
		return map[string]string{"status": "unsafe_link", "message": "Нужна публичная HTTPS-ссылка без пароля; можно написать название и город."}, nil
	}
	if p.City == "" {
		data, err := s.readContext(ctx, actor)
		if err != nil {
			return nil, err
		}
		p.City, _ = data.(map[string]any)["city"].(string)
	}
	if p.City == "" && len(links.FindAllString(turn.text, -1)) == 0 {
		return map[string]string{"status": "city_needed", "message": "Уточни город поиска. Профиль не меняется."}, nil
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
		if match != nil {
			if p.Mode == "discover" && !collection.AutomaticEligible(*match, float64(s.Store.Now().Unix())) {
				excluded++
				continue
			}
			result.Places = append(result.Places, match.ID)
			metadata = append(metadata, map[string]any{"already_in_collection": true, "id": match.ID, "title": match.Title, "frozen_until": match.FreezeUntil, "refused": match.Refused, "archived": match.Archived})
			continue
		}
		preview := "Найдено с помощью ИИ — проверь карточку перед сохранением.\n" + collection.Render(collection.Card{Input: card, Author: "ты, если добавишь"})
		if place.Why != "" {
			preview += "\nПочему подходит: " + limitText(place.Why, 250)
		}
		if len(place.Unknown) > 0 {
			preview += "\nНе подтверждено: " + strings.Join(place.Unknown, ", ")
		}
		if card.MapURL == "" {
			preview += "\nПоиск на Яндекс Картах (не точная отметка): https://yandex.ru/maps/?text=" + url.QueryEscape(card.Title+" "+card.City+" "+card.Address)
		}
		// Persist the caution and relevance together with the reviewed card, so
		// a saved incomplete card never turns into an apparently verified record.
		card.Description = strings.TrimSpace(card.Description + "\nПодготовлено ИИ по источникам; актуальность нужно проверить.")
		if place.Why != "" {
			card.Description += "\nПочему подходит: " + limitText(place.Why, 250)
		}
		if len(place.Unknown) > 0 {
			card.Description += "\nНе подтверждено: " + strings.Join(place.Unknown, ", ")
		}
		svc := actions.Service{Store: s.Store}
		raw, _ := json.Marshal(collection.SearchSave{Card: card})
		idea, err := svc.Prepare(ctx, actor, "ai:search_save", raw)
		if err != nil {
			return nil, err
		}
		candidate := ""
		if turn.pair != 0 {
			raw, _ = json.Marshal(collection.SearchSave{Card: card, Candidate: true})
			candidate, err = svc.Prepare(ctx, actor, "ai:search_save", raw)
			if err != nil {
				return nil, err
			}
		}
		prepared = append(prepared, SearchCard{Preview: preview, Ideas: idea, Candidates: candidate})
		metadata = append(metadata, map[string]any{"title": card.Title, "address": card.Address, "city": card.City, "price": card.Price, "price_basis": card.PriceBasis, "unknown": place.Unknown})
	}
	result.SearchCards = prepared
	summary := found.Summary
	if excluded > 0 {
		summary = "Некоторые найденные места уже есть в копилке и исключены из автоматического подбора из-за фриза, отказа или архива. Уточни условия или попроси другие варианты."
	}
	turn.value = map[string]any{"found": metadata, "summary": summary, "saved": false, "cards_prepared": len(prepared), "excluded": excluded, "instruction": "Карточки с источниками уже показаны отдельно. Не выдумывай больше вариантов; если совпадений мало или место неоднозначно, объясни и уточни."}
	if len(prepared) > 0 || len(result.Places) > 0 || excluded > 0 {
		titles := []string{}
		for _, p := range found.Places {
			titles = append(titles, p.Card.Title)
		}
		token, err := s.retry(ctx, actor, turn.pair, turn.conversation, "/search "+turn.text+"\nПредложи другие варианты вместо: "+strings.Join(titles, ", "))
		if err != nil {
			return nil, err
		}
		result.SearchButton = &SearchButton{Label: "Ещё варианты", Token: token}
	}
	return turn.value, nil
}
