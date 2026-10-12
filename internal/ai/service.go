// Package ai exposes scoped functions, never SQL, shell, unrestricted HTTP or
// committed mutations. Model calls may create drafts, but only user clicks apply.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

type Service struct {
	Store    *store.Store
	Provider Provider
	Search   SearchProvider
	Model    string
	Billing  Billing
	users    sync.Map
}
type Draft struct{ ID, Preview string }
type Result struct {
	Text         string
	Places       []int64
	Drafts       []Draft
	Retry        string
	SearchCards  []SearchCard
	SearchButton *SearchButton
}
type Query struct {
	Categories []string `json:"categories,omitempty"`
	Without    []string `json:"without,omitempty"`
	Budget     *float64 `json:"budget,omitempty"`
	Basis      string   `json:"basis,omitempty"`
	City       string   `json:"city,omitempty"`
}
type Place struct {
	ID              int64    `json:"id"`
	Title           string   `json:"title"`
	Categories      []string `json:"categories"`
	Address         string   `json:"address,omitempty"`
	City            string   `json:"city,omitempty"`
	Price           *float64 `json:"price,omitempty"`
	Basis           string   `json:"price_basis,omitempty"`
	Mutual          bool     `json:"mutual"`
	PartnerInterest string   `json:"partner_interest"`
}
type Selection struct {
	Places   []Place `json:"places"`
	Unknown  []Place `json:"budget_unverified"`
	Excluded int     `json:"excluded"`
}

var instruction = texts.T("internal.ai.service.001")

func decode(raw string, v any) error {
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid function arguments")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing function arguments")
	}
	return nil
}
func function(name, description string, props map[string]any, required []string) any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}}
}
func tools() []any {
	array := map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
	return []any{function("search_places", texts.T("internal.ai.service.002"), map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"discover", "identify"}}, "city": map[string]string{"type": "string"}}, []string{"mode"}), function("read_collection", texts.T("internal.ai.service.003"), map[string]any{"categories": array, "without": array, "city": map[string]string{"type": "string"}}, nil), function("read_context", texts.T("internal.ai.service.004"), map[string]any{}, nil), function("read_place", texts.T("internal.ai.service.005"), map[string]any{"id": map[string]string{"type": "integer"}}, []string{"id"}), function("suggest_places", texts.T("internal.ai.service.006"), map[string]any{"ids": map[string]any{"type": "array", "items": map[string]string{"type": "integer"}}}, []string{"ids"}), function("prepare_action", texts.T("internal.ai.service.007"), map[string]any{"kind": map[string]string{"type": "string"}, "payload": map[string]any{"type": "object"}}, []string{"kind", "payload"})}
}
func minimal(c collection.Card) Place {
	return Place{ID: c.ID, Title: c.Title, Categories: c.Categories, Address: c.Address, City: c.City, Price: c.Price, Basis: c.PriceBasis, Mutual: c.Mine == "want" && c.Theirs == "want", PartnerInterest: c.Theirs}
}
func has(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
func (s *Service) Select(ctx context.Context, actor int64, p Query) (Selection, error) {
	var result Selection
	if _, err := store.AIFor(ctx, s.Store.DB, actor); err != nil {
		return result, err
	}
	if p.Budget != nil && (math.IsNaN(*p.Budget) || math.IsInf(*p.Budget, 0) || *p.Budget < 0 || (p.Basis != "person" && p.Basis != "couple")) {
		return result, errors.New(texts.T("internal.ai.service.008"))
	}
	cards := collection.Service{Store: s.Store}
	list, err := cards.List(ctx, actor, "all")
	if err != nil {
		return result, err
	}
	now := float64(s.Store.Now().Unix())
	for _, c := range list {
		if !collection.AutomaticEligible(c, now) {
			result.Excluded++
			continue
		}
		if p.City != "" && c.City != "" && !strings.EqualFold(c.City, p.City) {
			continue
		}
		allowed := len(p.Categories) == 0
		for _, category := range p.Categories {
			if has(c.Categories, category) {
				allowed = true
			}
		}
		for _, category := range p.Without {
			if has(c.Categories, category) {
				allowed = false
			}
		}
		if !allowed {
			continue
		}
		place := minimal(c)
		if p.Budget != nil {
			if c.Price == nil {
				result.Unknown = append(result.Unknown, place)
				continue
			}
			amount := *c.Price
			if p.Basis == "couple" && c.PriceBasis == "person" {
				amount *= 2
			}
			if p.Basis == "person" && c.PriceBasis == "couple" {
				amount /= 2
			}
			if amount > *p.Budget {
				continue
			}
		}
		result.Places = append(result.Places, place)
	}
	sort.SliceStable(result.Places, func(i, j int) bool {
		return result.Places[i].Mutual && !result.Places[j].Mutual
	})
	return result, nil
}
func (s *Service) Converse(ctx context.Context, actor int64, text string) (Result, error) {
	// Serialize one caller's context rather than rejecting requests as a quota.
	value, _ := s.users.LoadOrStore(actor, new(sync.Mutex))
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	var result Result
	if s.Provider == nil {
		return result, errors.New("AI not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 12000 {
		return result, errors.New(texts.T("internal.ai.service.009"))
	}
	id, pair, history, err := s.begin(ctx, actor, text)
	if err != nil {
		return result, err
	}
	data, err := s.readContext(ctx, actor)
	personal := errors.Is(err, store.ErrDenied)
	if personal {
		user, e := store.UserFor(ctx, s.Store.DB, actor, false)
		if e != nil {
			return result, e
		}
		data = map[string]any{"city": user.City, "timezone": user.Timezone, "now_utc": s.Store.Now().UTC().Format(time.RFC3339)}
		err = nil
	}
	if err != nil {
		return result, err
	}
	contextJSON, _ := json.Marshal(data)
	// Provider context is bounded; the complete private history remains retained
	// for thirty days. This does not impose a request/spending quota.
	chars := 0
	start := len(history)
	for start > 0 {
		n := len([]rune(history[start-1].Content))
		if chars+n > 64000 && start < len(history) {
			break
		}
		chars += n
		start--
	}
	messages := append([]Message{{Role: "system", Content: instruction}, {Role: "user", Content: texts.T("internal.ai.service.010") + string(contextJSON)}}, history[start:]...)
	availableTools := tools()
	if personal {
		availableTools = []any{availableTools[0], availableTools[len(availableTools)-1]}
		messages[0].Content = instruction + texts.T("internal.ai.service.011")
	}
	allowed := map[int64]bool{}
	search := &searchTurn{text: text, conversation: id, pair: pair}
	// Links and explicit internet-search wording already define the lookup. Avoid a redundant
	// paid router-model call; native search interprets the human request.
	allowSearch, refuseSearch := searchPermission(text)
	if allowSearch && !refuseSearch {
		return s.directSearch(ctx, actor, search, result)
	}
	// Bounded tool rounds prevent runaway model loops. This is not a user quota;
	// ordinary followup requests remain available with no monthly cutoff.
	for round := 0; round < 8; round++ {
		e := s.current(ctx, actor, pair, id)
		if e != nil {
			return result, e
		}
		var usageID int64
		e = s.Store.Write(ctx, func(q store.Query) error {
			r, e := q.ExecContext(ctx, "INSERT INTO api_usage(user_id,provider,model,uncertain,success,created_at) VALUES(?,'deepseek',?,1,0,?)", actor, s.Model, s.Store.Now().Unix())
			if e != nil {
				return e
			}
			usageID, e = r.LastInsertId()
			return e
		})
		if e != nil {
			return result, e
		}
		completion, e := s.Provider.Complete(ctx, messages, availableTools)
		accountCtx, accountCancel := context.WithTimeout(context.Background(), 5*time.Second)
		accountErr := s.account(accountCtx, usageID, completion.Usage, e == nil)
		accountCancel()
		if accountErr != nil {
			return result, accountErr
		}
		if e != nil {
			token, err := s.retry(ctx, actor, pair, id, text)
			if err != nil {
				return result, err
			}
			result.Retry = token
			result.Text = texts.T("internal.ai.service.012")
			return result, nil
		}
		e = s.current(ctx, actor, pair, id)
		if e != nil {
			return result, e
		}
		messages = append(messages, completion.Message)
		if len(completion.Message.Calls) == 0 {
			result.Text = completion.Message.Content
			stored := result.Text
			if err = s.Store.Write(ctx, func(q store.Query) error {
				_, e := q.ExecContext(ctx, "INSERT INTO messages(conversation_id,role,text,created_at) VALUES(?,'assistant',?,?)", id, stored, s.Store.Now().Unix())
				return e
			}); err != nil {
				return result, err
			}
			return result, nil
		}
		for _, call := range completion.Message.Calls {
			if personal && call.Function.Name != "search_places" && call.Function.Name != "prepare_action" {
				return result, store.ErrDenied
			}
			var value any
			var e error
			if call.Function.Name == "search_places" {
				value, e = s.searchTool(ctx, actor, call.Function.Arguments, search, &result)
			} else {
				value, e = s.invoke(ctx, actor, call, &result, allowed)
			}
			var failed *searchFailure
			if errors.As(e, &failed) {
				token, err := s.retry(ctx, actor, pair, id, text)
				if err != nil {
					return result, err
				}
				result.Retry = token
				result.Text = texts.T("internal.ai.service.013")
				return result, nil
			}
			if e != nil {
				value = map[string]string{"error": texts.T("internal.ai.service.014")}
			}
			raw, _ := json.Marshal(value)
			messages = append(messages, Message{Role: "tool", CallID: call.ID, Content: string(raw)})
		}
		// Search results are already validated and rendered as separate cards.
		// Returning them immediately avoids a second verbose model paraphrase.
		if search.attempted && (len(result.SearchCards) > 0 || len(result.Places) > 0) {
			result.Text = searchResultTitle(search.mode, len(result.SearchCards)+len(result.Places))
			stored := result.Text
			if err = s.Store.Write(ctx, func(q store.Query) error {
				_, e := q.ExecContext(ctx, "INSERT INTO messages(conversation_id,role,text,created_at) VALUES(?,'assistant',?,?)", id, stored, s.Store.Now().Unix())
				return e
			}); err != nil {
				return result, err
			}
			return result, nil
		}
	}
	result.Text = texts.T("internal.ai.service.015")
	return result, nil
}
func (s *Service) invoke(ctx context.Context, actor int64, call Call, result *Result, allowed map[int64]bool) (any, error) {
	if _, err := store.AIFor(ctx, s.Store.DB, actor); err != nil && call.Function.Name != "prepare_action" {
		return nil, err
	}
	switch call.Function.Name {
	case "read_collection":
		var p Query
		if err := decode(call.Function.Arguments, &p); err != nil {
			return nil, err
		}
		selection, err := s.Select(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		clear(allowed)
		for _, p := range append(selection.Places, selection.Unknown...) {
			allowed[p.ID] = true
		}
		return selection, nil
	case "read_context":
		if err := decode(call.Function.Arguments, new(struct{})); err != nil {
			return nil, err
		}
		return s.readContext(ctx, actor)
	case "read_place":
		var p struct {
			ID int64 `json:"id"`
		}
		if err := decode(call.Function.Arguments, &p); err != nil {
			return nil, err
		}
		cards := collection.Service{Store: s.Store}
		c, err := cards.Get(ctx, s.Store.DB, actor, p.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"place": minimal(c), "version": c.Version, "frozen_until": c.FreezeUntil, "refused": c.Refused, "archived": c.Archived}, nil
	case "suggest_places":
		var p struct {
			IDs []int64 `json:"ids"`
		}
		if err := decode(call.Function.Arguments, &p); err != nil {
			return nil, err
		}
		cards := collection.Service{Store: s.Store}
		for _, id := range p.IDs {
			if !allowed[id] {
				return nil, store.ErrDenied
			}
			c, err := cards.Get(ctx, s.Store.DB, actor, id)
			if err != nil {
				return nil, err
			}
			if !collection.AutomaticEligible(c, float64(s.Store.Now().Unix())) {
				return nil, store.ErrConflict
			}
		}
		result.Places = append([]int64(nil), p.IDs...)
		return map[string]bool{"cards_prepared": true}, nil
	case "prepare_action":
		var p struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := decode(call.Function.Arguments, &p); err != nil {
			return nil, err
		}
		if _, err := store.AIFor(ctx, s.Store.DB, actor); err != nil {
			if _, e := store.AIConversationFor(ctx, s.Store.DB, actor); e != nil {
				return nil, e
			}
			switch p.Kind {
			case "place_create", "preferences":
			case "plan_save":
				var plan planning.Input
				if e := rawDecode(p.Payload, &plan); e != nil {
					return nil, e
				}
				if !plan.Draft || plan.ID != 0 || len(plan.Places) != 0 {
					return nil, store.ErrDenied
				}
			default:
				return nil, store.ErrDenied
			}
		}
		if p.Kind == "place_edit" {
			var edit collection.Edit
			if err := rawDecode(p.Payload, &edit); err != nil {
				return nil, err
			}
			cards := collection.Service{Store: s.Store}
			existing, err := cards.Get(ctx, s.Store.DB, actor, edit.ID)
			if err != nil {
				return nil, err
			}
			// The model cannot invent photos or sources, and an ordinary text edit
			// must not remove them simply because they were excluded from AI data.
			edit.Card.Photos = existing.Photos
			edit.Card.Sources = existing.Sources
			var fields struct {
				Card map[string]json.RawMessage `json:"card"`
			}
			_ = json.Unmarshal(p.Payload, &fields)
			if _, ok := fields.Card["title"]; !ok {
				edit.Card.Title = existing.Title
			}
			if _, ok := fields.Card["categories"]; !ok {
				edit.Card.Categories = existing.Categories
			}
			if _, ok := fields.Card["address"]; !ok {
				edit.Card.Address = existing.Address
			}
			if _, ok := fields.Card["city"]; !ok {
				edit.Card.City = existing.City
			}
			if _, ok := fields.Card["map_url"]; !ok {
				edit.Card.MapURL = existing.MapURL
			}
			if _, ok := fields.Card["description"]; !ok {
				edit.Card.Description = existing.Description
			}
			if _, ok := fields.Card["price"]; !ok {
				edit.Card.Price = existing.Price
				edit.Card.PriceBasis = existing.PriceBasis
			}
			p.Payload, _ = json.Marshal(edit)
		}
		preview, err := s.preview(ctx, actor, p.Kind, p.Payload)
		if err != nil {
			return nil, err
		}
		actions := actions.Service{Store: s.Store}
		id, err := actions.Prepare(ctx, actor, "ai:"+p.Kind, p.Payload)
		if err != nil {
			return nil, err
		}
		result.Drafts = append(result.Drafts, Draft{ID: id, Preview: preview})
		// A human preview may include retained notes/sources/photos. It is sent
		// only to Telegram, never reflected into the provider or chat history.
		return map[string]any{"draft_prepared": true, "saved": false}, nil
	default:
		return nil, store.ErrDenied
	}
}
func (s *Service) retry(ctx context.Context, actor, pair int64, conversation, text string) (string, error) {
	var id string
	err := s.Store.Write(ctx, func(q store.Query) error {
		// Retry payload is private to the caller and never contains an API key.
		var err error
		id, err = identity.RandomToken()
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]string{"conversation": conversation, "text": text})
		var scope any
		if pair != 0 {
			scope = pair
		}
		_, err = q.ExecContext(ctx, "INSERT INTO drafts(id,user_id,pair_id,kind,payload,created_at,expires_at) VALUES(?,?,?,'ai_retry',?,?,?)", id, actor, scope, string(raw), s.Store.Now().Unix(), s.Store.Now().Add(30*24*time.Hour).Unix())
		return err
	})
	return id, err
}

// bytes keeps strict decode consistent for raw action payloads.
func rawDecode(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing action arguments")
	}
	return nil
}
