package telegram

import (
	"context"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot/models"
)

func telegramName(u models.User) string {
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name == "" {
		return texts.T("internal.telegram.telegram_name.001")
	}
	return name
}
func (r *Router) syncTelegramName(ctx context.Context, u models.User) error {
	if strings.TrimSpace(u.FirstName) == "" {
		return nil
	}
	_, err := r.Store.DB.ExecContext(ctx, "UPDATE users SET name=? WHERE id=? AND name<>?", telegramName(u), u.ID, telegramName(u))
	return err
}
