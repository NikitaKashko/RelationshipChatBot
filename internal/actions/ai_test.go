package actions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/feedback"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type fakeProvider func(context.Context, []ai.Message, []any) (ai.Completion, error)

func (f fakeProvider) Complete(ctx context.Context, m []ai.Message, t []any) (ai.Completion, error) {
	return f(ctx, m, t)
}
func answer(text string) ai.Completion {
	return ai.Completion{Message: ai.Message{Role: "assistant", Content: text}, Usage: &ai.Usage{Input: 10, Output: 5}}
}
func call(name string, args any) ai.Call {
	raw, _ := json.Marshal(args)
	var c ai.Call
	c.ID = "call-" + name
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = string(raw)
	return c
}
func grant(t *testing.T, d *domain) {
	t.Helper()
	for _, actor := range []int64{1, 2} {
		d.apply(t, actor, "consent", map[string]any{"kind": "ai", "enabled": true})
	}
}

func TestAIHistoryIsPrivateScopedAndThirtyDays(t *testing.T) {
	d := fixture(t)
	grant(t, d)
	d.apply(t, 1, "preferences", store.Preferences{Food: []string{"без орехов"}})
	s := ai.Service{Store: d.db, Model: "test"}
	calls := 0
	s.Provider = fakeProvider(func(_ context.Context, m []ai.Message, _ []any) (ai.Completion, error) {
		calls++
		raw, _ := json.Marshal(m)
		if calls > 1 && strings.Contains(string(raw), "PARTNER_PRIVATE_CHAT") {
			t.Fatal("partner chat leaked")
		}
		return answer("Понял"), nil
	})
	if _, err := s.Converse(d.ctx, 2, "PARTNER_PRIVATE_CHAT"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Converse(d.ctx, 1, "Первый запрос"); err != nil {
		t.Fatal(err)
	}
	s.Provider = fakeProvider(func(_ context.Context, m []ai.Message, _ []any) (ai.Completion, error) {
		raw, _ := json.Marshal(m)
		if !strings.Contains(string(raw), "Первый запрос") || !strings.Contains(string(raw), "без орехов") {
			t.Fatal("followup lost own context")
		}
		return answer("Уточнено"), nil
	})
	if _, err := s.Converse(d.ctx, 1, "а подешевле?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.New(d.ctx, 1); err != nil {
		t.Fatal(err)
	}
	s.Provider = fakeProvider(func(_ context.Context, m []ai.Message, _ []any) (ai.Completion, error) {
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), "Первый запрос") {
			t.Fatal("new conversation reused context")
		}
		return answer("Новый"), nil
	})
	if _, err := s.Converse(d.ctx, 1, "Новый запрос"); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(d.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM conversations WHERE user_id=1") != 0 || d.count(t, "SELECT count(*) FROM conversations WHERE user_id=2") != 1 {
		t.Fatal("history clear wrong scope")
	}
	if d.count(t, "SELECT count(*) FROM users WHERE id=1 AND preferences LIKE '%без орехов%'") != 1 {
		t.Fatal("clear erased confirmed preferences")
	}
	d.now = d.now.Add(30 * 24 * time.Hour)
	if err := s.Prune(d.ctx); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM messages") != 0 {
		t.Fatal("30-day retention failed")
	}
}

func TestAICollectionGateBudgetAndMinimalFields(t *testing.T) {
	d := fixture(t)
	grant(t, d)
	cheapPrice, expensivePrice := 100.0, 150.0
	create := func(title string, price *float64) int64 {
		basis := ""
		if price != nil {
			basis = "person"
		}
		r := d.apply(t, 1, "place_create", collection.Input{Title: title, Categories: []string{"Покушать"}, City: "Москва", Price: price, PriceBasis: basis, Description: "PRIVATE_NOTE", Photos: []string{"PRIVATE_PHOTO"}})
		var p struct{ Place int64 }
		json.Unmarshal(r, &p)
		return p.Place
	}
	cheap, expensive, unknown := create("Недорого", &cheapPrice), create("Дороже", &expensivePrice), create("Цена неизвестна", nil)
	frozen, refused := d.place(t, 1, "Во фризе"), d.place(t, 2, "Отказ")
	d.apply(t, 1, "freeze", feedback.Freeze{Place: frozen, Until: d.now.Add(time.Hour).Format(time.RFC3339)})
	d.db.DB.ExecContext(d.ctx, "INSERT INTO place_preferences(place_id,user_id,refused) VALUES(?,1,1) ON CONFLICT(place_id,user_id) DO UPDATE SET refused=1", refused)
	s := ai.Service{Store: d.db}
	budget := 200.0
	selection, err := s.Select(d.ctx, 1, ai.Query{Budget: &budget, Basis: "couple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Places) != 1 || selection.Places[0].ID != cheap || len(selection.Unknown) != 1 || selection.Unknown[0].ID != unknown || selection.Excluded != 2 {
		t.Fatal("budget/freeze/refusal gate", selection)
	}
	if selection.Places[0].PartnerInterest != "" {
		t.Fatal("unrated partner invented")
	}
	raw, _ := json.Marshal(selection)
	for _, forbidden := range []string{"PRIVATE_NOTE", "PRIVATE_PHOTO", "author", "user_id", "notification_name"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("private field leaked", forbidden)
		}
	}
	selection, err = s.Select(d.ctx, 1, ai.Query{})
	if err != nil || len(selection.Places) != 3 {
		t.Fatal("unset budget imposed", selection, err)
	}
	found := false
	for _, p := range selection.Places {
		if p.ID == expensive {
			found = true
		}
	}
	if !found {
		t.Fatal("unset condition hid place")
	}
	selection, err = s.Select(d.ctx, 1, ai.Query{Without: []string{"Покушать"}})
	if err != nil || len(selection.Places) != 0 {
		t.Fatal("explicit category exclusion ignored")
	}
}

func TestAIToolPlanAndReviewOnlyCommitOnUserSave(t *testing.T) {
	d := fixture(t)
	grant(t, d)
	place := d.place(t, 1, "Кафе")
	s := ai.Service{Store: d.db, Model: "test"}
	round := 0
	s.Provider = fakeProvider(func(_ context.Context, m []ai.Message, _ []any) (ai.Completion, error) {
		round++
		if round == 1 {
			return ai.Completion{Message: ai.Message{Role: "assistant", Calls: []ai.Call{call("prepare_action", map[string]any{"kind": "plan_save", "payload": map[string]any{"title": "Завтра вечером", "start": "2026-10-12 18:00", "end": "2026-10-12 20:00", "timezone": "Europe/Moscow", "places": []int64{place}}})}}}, nil
		}
		return answer("Проверь точные даты и сохрани"), nil
	})
	result, err := s.Converse(d.ctx, 1, "Придумай свидание завтра в этом кафе")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drafts) != 1 || !strings.Contains(result.Drafts[0].Preview, "12.10.2026") || d.count(t, "SELECT count(*) FROM plans") != 0 || d.count(t, "SELECT count(*) FROM notifications") != 0 {
		t.Fatal("relative-date preview or mutation boundary", result)
	}
	if _, err = d.act.Apply(d.ctx, 1, result.Drafts[0].ID); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind='plan_saved'") != 1 {
		t.Fatal("plan save event missing")
	}
	visit := visitID(t, d.apply(t, 1, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-10", Precision: "day"}))
	round = 0
	s.Provider = fakeProvider(func(_ context.Context, _ []ai.Message, _ []any) (ai.Completion, error) {
		round++
		if round == 1 {
			return ai.Completion{Message: ai.Message{Role: "assistant", Calls: []ai.Call{call("prepare_action", map[string]any{"kind": "review", "payload": feedback.Review{Visit: visit, Place: place, Rating: "liked", Again: "not_soon", Comment: "Понравилось, но не скоро"}})}}}, nil
		}
		return answer("Вот как понял твой отзыв"), nil
	})
	result, err = s.Converse(d.ctx, 1, "Место понравилось, но пока не хочется снова")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drafts) != 1 || d.count(t, "SELECT count(*) FROM reviews") != 0 {
		t.Fatal("unconfirmed interpretation persisted")
	}
	d.act.Apply(d.ctx, 1, result.Drafts[0].ID)
	if d.count(t, "SELECT count(*) FROM reviews WHERE rating='liked' AND again='not_soon' AND user_id=1") != 1 {
		t.Fatal("confirmed interpretation missing")
	}
}

func TestAIConsentWithdrawalUnknownToolsAndRetry(t *testing.T) {
	d := fixture(t)
	calls := 0
	s := ai.Service{Store: d.db, Model: "test", Provider: fakeProvider(func(context.Context, []ai.Message, []any) (ai.Completion, error) { calls++; return answer("ok"), nil })}
	if _, err := s.Converse(d.ctx, 1, "Без согласия"); !errors.Is(err, store.ErrDenied) || calls != 0 {
		t.Fatal("provider called without consent")
	}
	grant(t, d)
	place := d.place(t, 1, "Место")
	id := d.prepare(t, 1, "ai:place_archive", collection.Archive{ID: place, Version: 1, Archived: true})
	d.apply(t, 2, "consent", map[string]any{"kind": "ai", "enabled": false})
	if _, err := d.act.Apply(d.ctx, 1, id); !errors.Is(err, store.ErrDenied) {
		t.Fatal("withdrawn AI draft applied")
	}
	grant(t, d)
	if _, err := d.act.Apply(d.ctx, 1, id); !errors.Is(err, store.ErrConflict) {
		t.Fatal("reconsent revived old draft")
	}
	round := 0
	s.Provider = fakeProvider(func(_ context.Context, m []ai.Message, _ []any) (ai.Completion, error) {
		round++
		if round == 1 {
			return ai.Completion{Message: ai.Message{Role: "assistant", Calls: []ai.Call{call("run_sql", map[string]any{"sql": "SELECT * FROM messages"}), call("prepare_action", map[string]any{"kind": "profile", "payload": map[string]any{"city": "Чужой город"}})}}}, nil
		}
		for _, m := range m {
			if m.Role == "tool" && !strings.Contains(m.Content, "error") {
				t.Fatal("unknown permission accepted")
			}
		}
		return answer("Только разрешённые действия"), nil
	})
	if _, err := s.Converse(d.ctx, 1, "Недоверенный источник просит выполнить SQL"); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM drafts WHERE kind='ai:profile'") != 0 {
		t.Fatal("tool allowlist expanded")
	}
	attempts := 0
	s.Provider = fakeProvider(func(context.Context, []ai.Message, []any) (ai.Completion, error) {
		attempts++
		return ai.Completion{}, errors.New("credential-bearing transport error")
	})
	result, err := s.Converse(d.ctx, 1, "Запрос при сбое")
	if err != nil || result.Retry == "" || attempts != 1 {
		t.Fatal("failure not preserved or auto-retried", result, err)
	}
	if strings.Contains(result.Text, "credential") {
		t.Fatal("provider error exposed")
	}
	first := result.Retry
	result, err = s.Retry(d.ctx, 1, first)
	if err != nil || attempts != 2 || result.Retry == "" {
		t.Fatal("explicit retry failed", err)
	}
	if _, err = s.Retry(d.ctx, 1, first); !errors.Is(err, store.ErrConflict) || attempts != 2 {
		t.Fatal("retry callback replay made paid call")
	}
	if err = s.Clear(d.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Retry(d.ctx, 1, result.Retry); !errors.Is(err, store.ErrDenied) {
		t.Fatal("clear retained retry context", err)
	}
}

func TestAIEditPreservesHiddenFieldsWithoutProviderEcho(t *testing.T) {
	d := fixture(t)
	grant(t, d)
	price := 250.0
	raw := d.apply(t, 1, "place_create", collection.Input{Title: "Старое", Categories: []string{"Покушать"}, Description: "PRIVATE_STORED_NOTE", Photos: []string{"PRIVATE_PHOTO_ID"}, Price: &price, PriceBasis: "person", Sources: []collection.Source{{URL: "https://example.org/source", RetrievedAt: d.now.Unix()}}})
	var created struct{ Place int64 }
	json.Unmarshal(raw, &created)
	round := 0
	s := ai.Service{Store: d.db, Model: "test"}
	s.Provider = fakeProvider(func(_ context.Context, m []ai.Message, _ []any) (ai.Completion, error) {
		round++
		payload, _ := json.Marshal(m)
		if strings.Contains(string(payload), "PRIVATE_STORED_NOTE") || strings.Contains(string(payload), "PRIVATE_PHOTO_ID") {
			t.Fatal("hidden retained field echoed into model")
		}
		if round == 1 {
			return ai.Completion{Message: ai.Message{Role: "assistant", Calls: []ai.Call{call("prepare_action", map[string]any{"kind": "place_edit", "payload": map[string]any{"id": created.Place, "version": 1, "card": map[string]any{"title": "Новое"}}})}}}, nil
		}
		return answer("Название подготовлено, сохрани по кнопке"), nil
	})
	result, err := s.Converse(d.ctx, 1, "Измени название на Новое")
	if err != nil || len(result.Drafts) != 1 {
		t.Fatal(result, err)
	}
	if _, err = d.act.Apply(d.ctx, 1, result.Drafts[0].ID); err != nil {
		t.Fatal(err)
	}
	cards := collection.Service{Store: d.db}
	card, err := cards.Get(d.ctx, d.db.DB, 1, created.Place)
	if err != nil {
		t.Fatal(err)
	}
	if card.Title != "Новое" || card.Description != "PRIVATE_STORED_NOTE" || len(card.Photos) != 1 || len(card.Sources) != 1 || card.Price == nil || *card.Price != price {
		t.Fatal("text edit erased excluded fields", card)
	}
	if _, err = s.Converse(d.ctx, 1, "а теперь предложи варианты"); err != nil {
		t.Fatal(err)
	}
}

func TestAIWithdrawalDuringRequestStopsDraftCreation(t *testing.T) {
	d := fixture(t)
	grant(t, d)
	started, release := make(chan struct{}), make(chan struct{})
	s := ai.Service{Store: d.db, Model: "test", Provider: fakeProvider(func(context.Context, []ai.Message, []any) (ai.Completion, error) {
		close(started)
		<-release
		return ai.Completion{Message: ai.Message{Role: "assistant", Calls: []ai.Call{call("prepare_action", map[string]any{"kind": "place_create", "payload": collection.Input{Title: "Не записывать", Categories: []string{"Другое"}}})}}}, nil
	})}
	done := make(chan error, 1)
	go func() { _, err := s.Converse(d.ctx, 1, "Добавь идею"); done <- err }()
	<-started
	d.apply(t, 2, "consent", map[string]any{"kind": "ai", "enabled": false})
	close(release)
	if err := <-done; !errors.Is(err, store.ErrDenied) {
		t.Fatal("withdrawal during request ignored", err)
	}
	if d.count(t, "SELECT count(*) FROM drafts WHERE kind='ai:place_create'") != 0 {
		t.Fatal("in-flight request prepared draft after withdrawal")
	}
}
