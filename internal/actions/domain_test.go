package actions_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type domain struct {
	db   *store.Store
	act  actions.Service
	ctx  context.Context
	now  time.Time
	pair int64
}

func fixture(t *testing.T) *domain {
	t.Helper()
	d := &domain{ctx: context.Background(), now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	var err error
	d.db, err = store.Open(d.ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.db.DB.Close() })
	d.db.Now = func() time.Time { return d.now }
	d.act.Store = d.db
	ids := identity.Service{Store: d.db}
	token, err := ids.Bootstrap(d.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{1, 2, 3} {
		if uid != 1 {
			token, err = ids.Issue(d.ctx, 1, "access")
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err = ids.Claim(d.ctx, uid, "User", token); err != nil {
			t.Fatal(err)
		}
		if err = d.db.Write(d.ctx, func(q store.Query) error {
			return ids.FinishProfile(d.ctx, q, uid, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	token, err = ids.Issue(d.ctx, 1, "partner")
	if err != nil {
		t.Fatal(err)
	}
	ids.Claim(d.ctx, 2, "User", token)
	d.pair, err = ids.Accept(d.ctx, 2, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.db.DB.ExecContext(d.ctx, "UPDATE pairs SET city='Москва',timezone='Europe/Moscow' WHERE id=?; DELETE FROM notifications", d.pair); err != nil {
		t.Fatal(err)
	}
	return d
}
func (d *domain) prepare(t *testing.T, actor int64, kind string, p any) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.act.Prepare(d.ctx, actor, kind, raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (d *domain) apply(t *testing.T, actor int64, kind string, p any) json.RawMessage {
	t.Helper()
	id := d.prepare(t, actor, kind, p)
	r, err := d.act.Apply(d.ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (d *domain) place(t *testing.T, actor int64, title string) int64 {
	t.Helper()
	r := d.apply(t, actor, "place_create", collection.Input{Title: title, Categories: []string{"Погулять", "Наше"}})
	var out struct{ Place int64 }
	if err := json.Unmarshal(r, &out); err != nil {
		t.Fatal(err)
	}
	return out.Place
}
func (d *domain) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := d.db.DB.QueryRowContext(d.ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddresslessDraftAndDuplicatePreservesAuthor(t *testing.T) {
	d := fixture(t)
	p := collection.Input{Title: "Пикник у озера", Categories: []string{"Погулять", "Наше", "Наше"}, MapURL: "https://yandex.ru/maps/org/park/123"}
	draft := d.prepare(t, 1, "place_create", p)
	if d.count(t, "SELECT count(*) FROM places") != 0 {
		t.Fatal("saved before confirm")
	}
	r, err := d.act.Apply(d.ctx, 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Place int64 }
	json.Unmarshal(r, &out)
	if _, err = d.act.Apply(d.ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM place_categories") != 2 {
		t.Fatal("duplicate categories")
	}
	p.MapURL += "?utm_source=test"
	dup := d.prepare(t, 2, "place_create", p)
	_, err = d.act.Apply(d.ctx, 2, dup)
	var existing *collection.Duplicate
	if !errors.As(err, &existing) || existing.ID != out.Place {
		t.Fatal("duplicate not detected", err)
	}
	d.apply(t, 2, "place_interest", collection.Interest{ID: out.Place, Interest: "want"})
	cards := collection.Service{Store: d.db}
	card, err := cards.Get(d.ctx, d.db.DB, 2, out.Place)
	if err != nil || card.AuthorID == nil || *card.AuthorID != 1 || card.Address != "" {
		t.Fatal("author or address changed", card, err)
	}
	mutual, err := cards.List(d.ctx, 1, "mutual")
	if err != nil || len(mutual) != 1 {
		t.Fatal("mutual filter", err)
	}
	if d.count(t, "SELECT count(*) FROM notifications") != 0 {
		t.Fatal("review queue created")
	}
	if _, err = cards.Get(d.ctx, d.db.DB, 3, out.Place); !errors.Is(err, store.ErrDenied) {
		t.Fatal("outsider card", err)
	}
}

func TestBranchesPhotosAndUnknownPrice(t *testing.T) {
	d := fixture(t)
	for _, address := range []string{"Улица 1", "Улица 2"} {
		d.apply(t, 1, "place_create", collection.Input{Title: "Кафе", Address: address, Categories: []string{"Покушать"}, Photos: []string{"file_one", "file_two", "file_three"}})
	}
	cards := collection.Service{Store: d.db}
	list, err := cards.List(d.ctx, 2, "all")
	if err != nil || len(list) != 2 {
		t.Fatal("branches merged", err)
	}
	if list[0].Price != nil || len(list[0].Photos) != 3 {
		t.Fatal("unknown price or photo count")
	}
	bad := collection.Input{Title: "Bad", Categories: []string{"Другое"}, Photos: []string{"a", "b", "c", "d"}}
	raw, _ := json.Marshal(bad)
	if _, err = d.act.Prepare(d.ctx, 1, "place_create", raw); err == nil {
		t.Fatal("too many photos accepted")
	}
	draft := d.prepare(t, 2, "place_create", collection.Input{Title: "Кафе", Address: "Улица 1", Categories: []string{"Покушать"}})
	_, err = d.act.Apply(d.ctx, 2, draft)
	var duplicate *collection.Duplicate
	if !errors.As(err, &duplicate) {
		t.Fatal("name/address duplicate missed", err)
	}
	price := 1200.50
	d.apply(t, 1, "place_create", collection.Input{Title: "Концерт", Categories: []string{"Музыка"}, Price: &price, PriceBasis: "person", Sources: []collection.Source{{URL: "https://example.com/event", RetrievedAt: d.now.Unix()}}})
	list, err = cards.List(d.ctx, 2, "all")
	if err != nil || list[0].Price == nil || *list[0].Price != price || len(list[0].Sources) != 1 {
		t.Fatal("price/source metadata lost", err)
	}
}

func TestPlanAtomicSaveConcurrencyAndSnapshots(t *testing.T) {
	d := fixture(t)
	a := d.place(t, 1, "Парк")
	b := d.place(t, 2, "Кафе")
	d.apply(t, 1, "candidate", planning.Candidate{ID: a})
	d.apply(t, 2, "candidate", planning.Candidate{ID: b})
	if d.count(t, "SELECT count(*) FROM notifications") != 0 {
		t.Fatal("candidate notified partner")
	}
	input := planning.Input{Title: "Вечер", Start: "2026-10-12 23:30", End: "2026-10-13 01:00", Timezone: "Europe/Moscow", Places: []int64{a, b}}
	draft := d.prepare(t, 1, "plan_save", input)
	if d.count(t, "SELECT count(*) FROM plans") != 0 {
		t.Fatal("plan saved before confirm")
	}
	r, err := d.act.Apply(d.ctx, 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Plan int64 }
	json.Unmarshal(r, &result)
	if _, err = d.act.Apply(d.ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind='plan_saved'") != 1 {
		t.Fatal("duplicate save notification")
	}
	plans := planning.Service{Store: d.db}
	saved, err := plans.Get(d.ctx, d.db.DB, 2, result.Plan)
	if err != nil || saved.Items[0].ID != a || saved.Items[1].ID != b {
		t.Fatal("lost order", err)
	}
	var feedback float64
	d.db.DB.QueryRowContext(d.ctx, "SELECT available_at FROM notifications WHERE kind='feedback' LIMIT 1").Scan(&feedback)
	want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	if int64(feedback) != want.Unix() {
		t.Fatal("wrong next day feedback", feedback, want)
	}
	input.ID = result.Plan
	input.Version = 1
	input.Title = "Изменение A"
	x := d.prepare(t, 1, "plan_save", input)
	input.Title = "Изменение B"
	y := d.prepare(t, 2, "plan_save", input)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, id := range []string{x, y} {
		wg.Add(1)
		go func(actor int64, id string) { defer wg.Done(); _, err := d.act.Apply(d.ctx, actor, id); results <- err }(int64(i+1), id)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatal("concurrent edits silently overwritten")
	}
	cards := collection.Service{Store: d.db}
	card, _ := cards.Get(d.ctx, d.db.DB, 2, a)
	d.apply(t, 2, "place_edit", collection.Edit{ID: a, Version: card.Version, Card: collection.Input{Title: "Переименованный парк", Categories: []string{"Погулять"}}})
	card, _ = cards.Get(d.ctx, d.db.DB, 2, a)
	d.apply(t, 2, "place_archive", collection.Archive{ID: a, Version: card.Version, Archived: true})
	saved, err = plans.Get(d.ctx, d.db.DB, 1, result.Plan)
	if err != nil || saved.Items[0].Title != "Парк" {
		t.Fatal("historical snapshot changed", err)
	}
}

func TestManualFrozenChoiceCancelAndRollback(t *testing.T) {
	d := fixture(t)
	place := d.place(t, 1, "Любимое место")
	if _, err := d.db.DB.ExecContext(d.ctx, "INSERT INTO freeze_overrides VALUES(?,?,1,?); UPDATE place_preferences SET refused=1 WHERE place_id=? AND user_id=1", place, d.now.Add(30*24*time.Hour).Unix(), d.now.Unix(), place); err != nil {
		t.Fatal(err)
	}
	p := planning.Input{Title: "Повтор", Start: "2026-10-12 18:00", End: "2026-10-12 20:00", Timezone: "Europe/Moscow", Places: []int64{place}}
	r := d.apply(t, 2, "plan_save", p)
	var out struct{ Plan int64 }
	json.Unmarshal(r, &out)
	if d.count(t, "SELECT count(*) FROM freeze_overrides") != 1 || d.count(t, "SELECT count(*) FROM place_preferences WHERE refused=1") != 1 {
		t.Fatal("manual choice cleared freeze/refusal")
	}
	d.apply(t, 1, "plan_cancel", planning.Cancel{ID: out.Plan, Version: 1})
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind IN ('reminder','feedback') AND status='pending'") != 0 {
		t.Fatal("jobs not cancelled")
	}
	if d.count(t, "SELECT count(*) FROM visits") != 0 {
		t.Fatal("cancellation fabricated visit")
	}
	// Fail after plan row update (items FK trigger) and ensure the transaction,
	// old jobs and old snapshots all survive without a partial notification.
	p.ID = 0
	p.Title = "Rollback test"
	draft := d.prepare(t, 2, "plan_save", p)
	d.db.DB.ExecContext(d.ctx, `CREATE TRIGGER fail_items BEFORE INSERT ON plan_items BEGIN SELECT RAISE(ABORT,'forced item failure'); END`)
	if _, err := d.act.Apply(d.ctx, 2, draft); err == nil {
		t.Fatal("fixture did not fail")
	}
	if d.count(t, "SELECT count(*) FROM plans") != 1 || d.count(t, "SELECT count(*) FROM notifications WHERE kind='plan_saved'") != 1 {
		t.Fatal("partial save remained")
	}
}

func TestDatesRequireExactValidInstants(t *testing.T) {
	for _, p := range []planning.Input{{Start: "завтра", End: "позже", Timezone: "Europe/Moscow"}, {Start: "2026-10-25 02:30", End: "2026-10-25 03:30", Timezone: "Europe/Berlin"}, {Start: "2026-03-29 02:30", End: "2026-03-29 04:30", Timezone: "Europe/Berlin"}, {Start: "2026-10-12 20:00", End: "2026-10-12 19:00", Timezone: "Europe/Moscow"}} {
		if _, _, err := planning.Dates(p); err == nil {
			t.Fatal("invalid/ambiguous schedule accepted", p)
		}
	}
	if _, _, err := planning.Dates(planning.Input{Start: "2026-10-25T02:30:00+02:00", End: "2026-10-25T03:30:00+01:00", Timezone: "Europe/Berlin"}); err != nil {
		t.Fatal("explicit DST offset rejected", err)
	}
}

func TestLocationSecondConfirmationRecomputesOnlyFutureFeedback(t *testing.T) {
	d := fixture(t)
	id := d.place(t, 1, "Место")
	r := d.apply(t, 1, "plan_save", planning.Input{Title: "План", Start: "2026-10-12 23:00", End: "2026-10-13 00:30", Timezone: "Europe/Moscow", Places: []int64{id}})
	var out struct{ Plan int64 }
	json.Unmarshal(r, &out)
	var before, after float64
	d.db.DB.QueryRowContext(d.ctx, "SELECT start_at FROM plans WHERE id=?", out.Plan).Scan(&before)
	r = d.apply(t, 1, "location_proposal", identity.Location{City: "Берлин", Timezone: "Europe/Berlin"})
	var proposal struct {
		Version int64 `json:"location_version"`
	}
	json.Unmarshal(r, &proposal)
	x := d.prepare(t, 1, "location_confirm", identity.LocationConfirmation{Version: proposal.Version})
	if _, err := d.act.Apply(d.ctx, 1, x); !errors.Is(err, store.ErrDenied) {
		t.Fatal("proposer approved own city", err)
	}
	x = d.prepare(t, 2, "location_confirm", identity.LocationConfirmation{Version: proposal.Version - 1})
	if _, err := d.act.Apply(d.ctx, 2, x); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale location confirmation", err)
	}
	d.apply(t, 2, "location_confirm", identity.LocationConfirmation{Version: proposal.Version})
	d.db.DB.QueryRowContext(d.ctx, "SELECT start_at FROM plans WHERE id=?", out.Plan).Scan(&after)
	if before != after {
		t.Fatal("timezone change moved plan")
	}
	var feedback float64
	d.db.DB.QueryRowContext(d.ctx, "SELECT available_at FROM notifications WHERE kind='feedback' LIMIT 1").Scan(&feedback)
	loc, _ := time.LoadLocation("Europe/Berlin")
	want := time.Date(2026, 10, 13, 12, 0, 0, 0, loc)
	if int64(feedback) != want.Unix() {
		t.Fatal("feedback calendar did not change", feedback, want)
	}
}

func TestFiltersAndSharedEditsKeepPersonalOpinions(t *testing.T) {
	d := fixture(t)
	mine := d.place(t, 1, "Мой парк")
	theirs := d.place(t, 2, "Её кафе")
	cards := collection.Service{Store: d.db}
	for filter, want := range map[string]int{"all": 2, "mine": 1, "partner": 1, "mutual": 0, "frozen": 0, "visited": 0, "archive": 0} {
		list, err := cards.List(d.ctx, 1, filter)
		if err != nil || len(list) != want {
			t.Fatal(filter, len(list), err)
		}
	}
	d.apply(t, 2, "place_interest", collection.Interest{ID: mine, Interest: "uninterested"})
	c, _ := cards.Get(d.ctx, d.db.DB, 1, mine)
	d.apply(t, 2, "place_edit", collection.Edit{ID: mine, Version: c.Version, Card: collection.Input{Title: "Обновлённый парк", Categories: []string{"Новая категория"}}})
	c, _ = cards.Get(d.ctx, d.db.DB, 1, mine)
	if c.Mine != "want" || c.Theirs != "uninterested" || c.AuthorID == nil || *c.AuthorID != 1 {
		t.Fatal("shared edit changed personal opinion", c)
	}
	d.apply(t, 1, "place_archive", collection.Archive{ID: theirs, Version: 1, Archived: true})
	list, err := cards.List(d.ctx, 1, "archive")
	if err != nil || len(list) != 1 || list[0].ID != theirs {
		t.Fatal("archive filter", err)
	}
	d.apply(t, 1, "place_archive", collection.Archive{ID: theirs, Version: 2, Archived: false})
	if _, err = d.db.DB.ExecContext(d.ctx, "INSERT INTO freeze_overrides VALUES(?,?,1,?)", mine, d.now.Add(24*time.Hour).Unix(), d.now.Unix()); err != nil {
		t.Fatal(err)
	}
	list, err = cards.List(d.ctx, 1, "frozen")
	if err != nil || len(list) != 1 {
		t.Fatal("freeze filter", err)
	}
	r, err := d.db.DB.ExecContext(d.ctx, "INSERT INTO visits(pair_id,marked_by,visit_date,visited_at,created_at) VALUES(?,1,'2026-10-10',?,?)", d.pair, d.now.Add(-24*time.Hour).Unix(), d.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	visit, _ := r.LastInsertId()
	if _, err = d.db.DB.ExecContext(d.ctx, "INSERT INTO visit_items VALUES(?,?)", visit, theirs); err != nil {
		t.Fatal(err)
	}
	list, err = cards.List(d.ctx, 1, "visited")
	if err != nil || len(list) != 1 || list[0].ID != theirs {
		t.Fatal("visited filter", err)
	}
}
