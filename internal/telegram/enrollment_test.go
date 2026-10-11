package telegram

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func TestEnrollmentSurvivesRestartAndRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ids := identity.Service{Store: db}
	token, err := ids.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ids.Claim(ctx, 1, "Никита", token); err != nil {
		t.Fatal(err)
	}
	e := Enrollment{Store: db}
	w, err := e.Begin(ctx, 1)
	if err != nil || w.Stage != "city" {
		t.Fatal(w, err)
	}
	if _, err = e.Input(ctx, 1, "Москва"); err != nil {
		t.Fatal(err)
	}
	db.DB.Close()
	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	e.Store = db
	w, err = e.Begin(ctx, 1)
	if err != nil || w.Stage != "name" || w.Profile.City != "Москва" {
		t.Fatal("wizard lost", w, err)
	}
	if _, err = e.Input(ctx, 1, "зайка"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Input(ctx, 1, "Wrong/Zone"); err == nil {
		t.Fatal("invalid timezone accepted")
	}
	w, err = e.Input(ctx, 1, "Europe/Moscow")
	if err != nil || w.Stage != "preview" {
		t.Fatal(w, err)
	}
	draft, err := e.ProfileDraft(ctx, 1, w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.UserFor(ctx, db.DB, 1, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("profile written without confirmation", err)
	}
	svc := actions.Service{Store: db}
	if _, err = svc.Apply(ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	u, err := store.UserFor(ctx, db.DB, 1, false)
	if err != nil || u.City != "Москва" || u.NotificationName != "зайка" {
		t.Fatal(u, err)
	}
	if _, err = svc.Apply(ctx, 1, draft); err != nil {
		t.Fatal("repeated save failed", err)
	}
}

func TestPartnerInvitationContinuesAfterEnrollment(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	ids := identity.Service{Store: db}
	owner, _ := ids.Bootstrap(ctx)
	ids.Claim(ctx, 1, "Owner", owner)
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 1, identity.Profile{City: "Москва", Timezone: "Europe/Moscow"})
	}); err != nil {
		t.Fatal(err)
	}
	invite, err := ids.Issue(ctx, 1, "partner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ids.Claim(ctx, 2, "Member", invite); err != nil {
		t.Fatal(err)
	}
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 2, identity.Profile{City: "Казань", Timezone: "Europe/Moscow"})
	}); err != nil {
		t.Fatal(err)
	}
	e := Enrollment{Store: db}
	hash, _, err := e.PendingPartner(ctx, 2)
	if err != nil || hash != identity.Hash(invite) {
		t.Fatal("lost pair invitation", err)
	}
	p, err := store.PairFor(ctx, db.DB, 2)
	if err != nil || p != 0 {
		t.Fatal("pair created without confirmation", err)
	}
	if _, err = ids.Accept(ctx, 2, invite); err != nil {
		t.Fatal(err)
	}
	hash, _, err = e.PendingPartner(ctx, 2)
	if err != nil || hash != "" {
		t.Fatal("consumed invite still shown", err)
	}
}
