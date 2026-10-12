package identity_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type fixture struct {
	db  *store.Store
	ids *identity.Service
	ctx context.Context
	now time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{ctx: context.Background(), now: time.Unix(1800000000, 0)}
	var err error
	f.db, err = store.Open(f.ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.DB.Close() })
	f.db.Now = func() time.Time { return f.now }
	f.ids = &identity.Service{Store: f.db}
	token, err := f.ids.Bootstrap(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.enroll(t, 1, token)
	return f
}
func (f *fixture) enroll(t *testing.T, id int64, token string) {
	t.Helper()
	if _, err := f.ids.Claim(f.ctx, id, "User", token); err != nil {
		t.Fatal(err)
	}
	err := f.db.Write(f.ctx, func(q store.Query) error {
		return f.ids.FinishProfile(f.ctx, q, id, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
	})
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) member(t *testing.T, id int64) {
	t.Helper()
	token, err := f.ids.Issue(f.ctx, 1, "access")
	if err != nil {
		t.Fatal(err)
	}
	f.enroll(t, id, token)
}
func (f *fixture) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := f.db.DB.QueryRowContext(f.ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInvitationRaceResumeExpiryAndRevoke(t *testing.T) {
	f := setup(t)
	token, err := f.ids.Issue(f.ctx, 1, "access")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int64, 2)
	for _, id := range []int64{2, 3} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if _, err := f.ids.Claim(f.ctx, id, "Member", token); err == nil {
				results <- id
			}
		}(id)
	}
	wg.Wait()
	close(results)
	var winner int64
	for id := range results {
		if winner != 0 {
			t.Fatal("two accounts claimed one invite")
		}
		winner = id
	}
	if winner == 0 {
		t.Fatal("neither claim succeeded")
	}
	if _, err = f.ids.Claim(f.ctx, winner, "Member", token); err != nil {
		t.Fatal("cannot resume", err)
	}
	f.now = f.now.Add(7 * 24 * time.Hour)
	err = f.db.Write(f.ctx, func(q store.Query) error {
		return f.ids.FinishProfile(f.ctx, q, winner, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
	})
	if !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired enrollment accepted", err)
	}
	next, err := f.ids.Issue(f.ctx, 1, "access")
	if err != nil {
		t.Fatal(err)
	}
	f.enroll(t, winner, next)
	revoked, err := f.ids.Issue(f.ctx, 1, "access")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ids.Revoke(f.ctx, 1, revoked); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ids.Claim(f.ctx, 4, "Member", revoked); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked invite accepted", err)
	}
	if f.count(t, "SELECT count(*) FROM users") != 2 {
		t.Fatal("failed claims left profiles")
	}
}

func TestMemberReferralIsSingleUseWhileOwnerRemainsUnlimited(t *testing.T) {
	f := setup(t)
	f.member(t, 2)

	first, err := f.ids.Issue(f.ctx, 2, "access")
	if err != nil {
		t.Fatal("member could not issue referral", err)
	}
	if _, err = f.ids.Issue(f.ctx, 2, "access"); !errors.Is(err, identity.ErrAccessInviteActive) {
		t.Fatal("second active referral was allowed", err)
	}
	if err = f.ids.CancelAccess(f.ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ids.Claim(f.ctx, 3, "Ignored", first); !errors.Is(err, store.ErrConflict) {
		t.Fatal("cancelled referral was accepted", err)
	}
	second, err := f.ids.Issue(f.ctx, 2, "access")
	if err != nil {
		t.Fatal("replacement referral not issued", err)
	}
	if _, err = f.ids.Claim(f.ctx, 3, "Referred", second); err != nil {
		t.Fatal("referral was not claimed", err)
	}
	if _, err = f.ids.Issue(f.ctx, 2, "access"); !errors.Is(err, identity.ErrReferralUsed) {
		t.Fatal("member issued a second successful referral", err)
	}
	f.enroll(t, 3, second)
	if _, err = f.ids.Issue(f.ctx, 3, "access"); err != nil {
		t.Fatal("referred user did not receive their own referral", err)
	}
	if _, err = f.ids.Issue(f.ctx, 2, "partner"); err != nil {
		t.Fatal("access referral incorrectly consumed partner invitation", err)
	}
	if _, err = f.ids.Issue(f.ctx, 1, "access"); err != nil {
		t.Fatal("owner access invitation unexpectedly limited", err)
	}
	if _, err = f.ids.Issue(f.ctx, 1, "access"); err != nil {
		t.Fatal("owner could not issue multiple access invitations", err)
	}
}

func TestPairRaceAndDraftReceipt(t *testing.T) {
	f := setup(t)
	f.member(t, 2)
	f.member(t, 3)
	a, err := f.ids.Issue(f.ctx, 1, "partner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.ids.Issue(f.ctx, 3, "partner")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{a, b} {
		if _, err = f.ids.Claim(f.ctx, 2, "Member", token); err != nil {
			t.Fatal(err)
		}
	}
	svc := actions.Service{Store: f.db}
	raw, _ := json.Marshal(map[string]string{"hash": identity.Hash(a)})
	draft, err := svc.Prepare(f.ctx, 2, "pair_accept", raw)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.Apply(f.ctx, 2, draft)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Apply(f.ctx, 2, draft)
	if err != nil || string(receipt) != string(again) {
		t.Fatal("callback is not idempotent", err)
	}
	if _, err = f.ids.Accept(f.ctx, 2, b); !errors.Is(err, store.ErrConflict) {
		t.Fatal("competing pair accepted", err)
	}
	if f.count(t, "SELECT count(*) FROM pairs") != 1 || f.count(t, "SELECT count(*) FROM notifications") != 1 {
		t.Fatal("duplicate pair or notification")
	}
	if _, err = svc.Apply(f.ctx, 3, draft); !errors.Is(err, store.ErrDenied) {
		t.Fatal("foreign callback allowed", err)
	}
	// End the pair directly as a test fixture. A prior receipt no longer authorizes access.
	if _, err = f.db.DB.ExecContext(f.ctx, "UPDATE pairs SET status='ended'; UPDATE pair_members SET active=0"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Apply(f.ctx, 2, draft); !errors.Is(err, store.ErrConflict) {
		t.Fatal("ended pair receipt allowed", err)
	}
}

func TestConcurrentPairAcceptances(t *testing.T) {
	f := setup(t)
	f.member(t, 2)
	f.member(t, 3)
	a, _ := f.ids.Issue(f.ctx, 1, "partner")
	b, _ := f.ids.Issue(f.ctx, 3, "partner")
	for _, token := range []string{a, b} {
		if _, err := f.ids.Claim(f.ctx, 2, "Member", token); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, token := range []string{a, b} {
		wg.Add(1)
		go func(token string) { defer wg.Done(); _, err := f.ids.Accept(f.ctx, 2, token); results <- err }(token)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 || f.count(t, "SELECT count(*) FROM pairs") != 1 {
		t.Fatal("race created multiple pairs")
	}
}

func TestPairCreationMergesBothPersonalCollectionsAndPreservesAuthors(t *testing.T) {
	f := setup(t)
	f.member(t, 2)
	svc := actions.Service{Store: f.db}
	for actor, title := range map[int64]string{1: "Парк первого", 2: "Кафе второго"} {
		raw, _ := json.Marshal(collection.Input{Title: title, Categories: []string{"Идея"}})
		draft, err := svc.Prepare(f.ctx, actor, "place_create", raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Apply(f.ctx, actor, draft); err != nil {
			t.Fatal(err)
		}
	}
	token, err := f.ids.Issue(f.ctx, 1, "partner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ids.Claim(f.ctx, 2, "User", token); err != nil {
		t.Fatal(err)
	}
	pair, err := f.ids.Accept(f.ctx, 2, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []int64{1, 2} {
		list, err := (&collection.Service{Store: f.db}).List(f.ctx, actor, "all")
		if err != nil || len(list) != 2 {
			t.Fatalf("actor %d did not get merged collection: len=%d err=%v", actor, len(list), err)
		}
	}
	where := " WHERE pair_id=" + strconv.FormatInt(pair, 10)
	if f.count(t, "SELECT count(*) FROM places"+where+" AND personal_user_id IS NULL") != 2 {
		t.Fatal("personal places were not moved to pair scope")
	}
	if f.count(t, "SELECT count(DISTINCT author_id) FROM places"+where) != 2 {
		t.Fatal("original place authors were not preserved")
	}
}

func TestDraftBoundaryRollbackAndScope(t *testing.T) {
	f := setup(t)
	svc := actions.Service{Store: f.db}
	draft, err := svc.Prepare(f.ctx, 1, "consent", []byte(`{"kind":"ai","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.count(t, "SELECT count(*) FROM consents") != 0 {
		t.Fatal("prepare wrote consent")
	}
	if _, err = svc.Prepare(f.ctx, 1, "consent", []byte(`{"actor":2,"kind":"ai","enabled":true}`)); err == nil {
		t.Fatal("accepted model actor")
	}
	if _, err = svc.Apply(f.ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Apply(f.ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	if f.count(t, "SELECT count(*) FROM consents") != 1 {
		t.Fatal("duplicate consent")
	}
	forced := errors.New("forced rollback")
	err = f.db.Write(f.ctx, func(q store.Query) error {
		if _, err := q.ExecContext(f.ctx, "UPDATE users SET city='Wrong' WHERE id=1"); err != nil {
			return err
		}
		return forced
	})
	if !errors.Is(err, forced) {
		t.Fatal(err)
	}
	u, err := store.UserFor(f.ctx, f.db.DB, 1, false)
	if err != nil || u.City != "Москва" {
		t.Fatal("rollback lost city", err)
	}
	f.member(t, 2)
	f.member(t, 3)
	f.member(t, 4)
	token, _ := f.ids.Issue(f.ctx, 1, "partner")
	f.ids.Claim(f.ctx, 2, "Member", token)
	pair, err := f.ids.Accept(f.ctx, 2, token)
	if err != nil {
		t.Fatal(err)
	}
	token, _ = f.ids.Issue(f.ctx, 3, "partner")
	f.ids.Claim(f.ctx, 4, "Member", token)
	if _, err = f.ids.Accept(f.ctx, 4, token); err != nil {
		t.Fatal(err)
	}
	r, err := f.db.DB.ExecContext(f.ctx, "INSERT INTO places(author_id,pair_id,title,created_at) VALUES(1,?,'Park',?)", pair, f.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := r.LastInsertId()
	if err = store.PlaceFor(f.ctx, f.db.DB, 2, pid); err != nil {
		t.Fatal(err)
	}
	if err = store.PlaceFor(f.ctx, f.db.DB, 3, pid); !errors.Is(err, store.ErrDenied) {
		t.Fatal("cross pair access", err)
	}
	r, err = f.db.DB.ExecContext(f.ctx, "INSERT INTO plans(pair_id,author_id,title,start_at,end_at,timezone,created_at) VALUES(?,1,'Date',?,?, 'Europe/Moscow',?)", pair, f.now.Unix(), f.now.Unix()+3600, f.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := r.LastInsertId()
	if err = store.PlanFor(f.ctx, f.db.DB, 2, plan); err != nil {
		t.Fatal(err)
	}
	if err = store.PlanFor(f.ctx, f.db.DB, 4, plan); !errors.Is(err, store.ErrDenied) {
		t.Fatal("foreign plan accessible", err)
	}
	// SQLite constraints serialize the invariant even if another caller bypasses a service.
	err = f.db.Write(f.ctx, func(q store.Query) error {
		r, e := q.ExecContext(f.ctx, "INSERT INTO pairs(created_at) VALUES(?)", f.now.Unix())
		if e != nil {
			return e
		}
		other, _ := r.LastInsertId()
		_, e = q.ExecContext(f.ctx, "INSERT INTO pair_members(pair_id,user_id) VALUES(?,1)", other)
		return e
	})
	if err == nil || f.count(t, "SELECT count(*) FROM pairs") != 2 {
		t.Fatal("single-pair constraint or rollback failed")
	}
}
