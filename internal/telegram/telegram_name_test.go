package telegram

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/go-telegram/bot/models"
)

func TestTelegramNameRefreshDoesNotChangeRecipientNickname(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	ids := identity.Service{Store: db}
	token, _ := ids.Bootstrap(ctx)
	if _, err = ids.Claim(ctx, 1, "Old", token); err != nil {
		t.Fatal(err)
	}
	db.DB.ExecContext(ctx, "UPDATE users SET partner_nickname='зайка' WHERE id=1")
	r := Router{Store: db}
	r.syncTelegramName(ctx, models.User{ID: 1, FirstName: " Никита ", LastName: " Кашко "})
	var name, nickname string
	if err = db.DB.QueryRowContext(ctx, "SELECT name,partner_nickname FROM users WHERE id=1").Scan(&name, &nickname); err != nil {
		t.Fatal(err)
	}
	if name != "Никита Кашко" || nickname != "зайка" {
		t.Fatal(name, nickname)
	}
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 1, identity.Profile{City: "Москва", Timezone: "Europe/Moscow", Name: "Sender override"})
	}); err != nil {
		t.Fatal(err)
	}
	var oldField string
	db.DB.QueryRowContext(ctx, "SELECT notification_name FROM users WHERE id=1").Scan(&oldField)
	if oldField != "Никита Кашко" {
		t.Fatal("profile accepted sender alias")
	}
}
