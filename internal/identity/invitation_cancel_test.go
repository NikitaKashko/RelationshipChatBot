package identity_test

import (
	"errors"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"testing"
)

func TestAccessOwnerOnlyAndPartnerReissue(t *testing.T) {
	f := setup(t)
	f.member(t, 2)
	if _, e := f.ids.Issue(f.ctx, 2, "access"); !errors.Is(e, store.ErrDenied) {
		t.Fatal("member issued access", e)
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
	if _, e = f.ids.Accept(f.ctx, 3, current); e != nil {
		t.Fatal(e)
	}
	if f.count(t, "SELECT count(*) FROM pairs WHERE status='active'") != 1 {
		t.Fatal("partner link did not register and pair")
	}
}
