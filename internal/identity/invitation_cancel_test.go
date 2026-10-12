package identity_test

import (
	"errors"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func TestMemberAccessAndPartnerReissueAreIndependent(t *testing.T) {
	f := setup(t)
	f.member(t, 2)
	if _, e := f.ids.Issue(f.ctx, 2, "access"); e != nil {
		t.Fatal("member could not issue referral access", e)
	}
	old, e := f.ids.Issue(f.ctx, 2, "partner")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.ids.CancelPartners(f.ctx, 2); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ids.Claim(f.ctx, 3, "Partner", old); !errors.Is(e, store.ErrConflict) {
		t.Fatal("revoked link worked", e)
	}
	current, e := f.ids.Issue(f.ctx, 2, "partner")
	if e != nil {
		t.Fatal(e)
	}
	f.enroll(t, 3, current)
	if f.count(t, "SELECT count(*) FROM notifications WHERE kind='partner_registered' AND recipient_id=2") != 1 {
		t.Fatal("missing registration notification")
	}
	if err := f.db.Write(f.ctx, func(q store.Query) error {
		return f.ids.FinishProfile(f.ctx, q, 3, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
	}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, "SELECT count(*) FROM notifications WHERE kind='partner_registered'") != 1 {
		t.Fatal("duplicate registration notification")
	}
	if _, e = f.ids.Accept(f.ctx, 3, current); e != nil {
		t.Fatal(e)
	}
	if f.count(t, "SELECT count(*) FROM pairs WHERE status='active'") != 1 {
		t.Fatal("partner link did not register and pair")
	}
}
