package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
)

func (r *Router) NotificationSender(b *bot.Bot) notifications.Sender {
	return func(ctx context.Context, m notifications.Message) (int64, error) {
		var p struct {
			Summary  string   `json:"summary"`
			Changed  []string `json:"changed"`
			Plan     int64    `json:"plan_id"`
			Visit    int64    `json:"visit_id"`
			Place    int64    `json:"place_id"`
			Title    string   `json:"title"`
			City     string   `json:"city"`
			Timezone string   `json:"timezone"`
			Until    string   `json:"until"`
			Text     string   `json:"text"`
		}
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			return 0, notifications.ErrPermanent
		}
		text := ""
		switch m.Kind {
		case "plan_saved":
			text = texts.T("internal.telegram.notifications.001") + strings.Join(p.Changed, ", ") + "\n" + p.Summary
			if strings.HasPrefix(p.Summary, "📝") {
				text = texts.T("internal.telegram.notifications.002") + p.Summary
			}
		case "plan_cancelled":
			text = texts.T("internal.telegram.notifications.003") + p.Title + texts.T("internal.telegram.notifications.004")
		case "plan_not_went":
			text = fmt.Sprintf(texts.T("internal.telegram.notifications.005"), p.Plan)
		case "reminder":
			text = fmt.Sprintf(texts.T("internal.telegram.notifications.006"), p.Plan)
		case "feedback":
			var visit int64
			if err := r.Store.DB.QueryRowContext(ctx, "SELECT coalesce(max(id),0) FROM visits WHERE plan_id=? AND pair_id=? AND valid=1", p.Plan, m.Pair).Scan(&visit); err != nil {
				return 0, notifications.ErrPermanent
			}
			if visit != 0 {
				text = fmt.Sprintf(texts.T("internal.telegram.notifications.007"), visit)
			} else {
				text = fmt.Sprintf(texts.T("internal.telegram.notifications.008"), p.Plan)
			}
		case "freeze_changed":
			text = fmt.Sprintf(texts.T("internal.telegram.notifications.009"), p.Place, p.Until)
			if p.Until == "" {
				text = fmt.Sprintf(texts.T("internal.telegram.notifications.010"), p.Place)
			}
		case "visit_corrected":
			text = fmt.Sprintf(texts.T("internal.telegram.notifications.011"), p.Visit)
		case "location_proposal":
			text = texts.T("internal.telegram.notifications.012") + p.City + " (" + p.Timezone + texts.T("internal.telegram.notifications.013")
		case "pair_created":
			text = texts.T("internal.telegram.notifications.014")
		case "affection", "budget_target", "partner_registered", "release_announcement":
			text = p.Text
		default:
			return 0, notifications.ErrPermanent
		}
		// One outbox row sends one Telegram message, with no partial multi-send.
		runes := []rune(text)
		if len(runes) > 3500 {
			text = string(runes[:3500]) + texts.T("internal.telegram.notifications.015")
		}
		msg, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Recipient, Text: text})
		if err == nil {
			return int64(msg.ID), nil
		}
		if errors.Is(err, bot.ErrorForbidden) || errors.Is(err, bot.ErrorBadRequest) || errors.Is(err, bot.ErrorUnauthorized) || errors.Is(err, bot.ErrorNotFound) {
			return 0, notifications.ErrPermanent
		}
		var retry *bot.TooManyRequestsError
		if errors.As(err, &retry) {
			return 0, &notifications.RetryError{After: time.Duration(retry.RetryAfter) * time.Second}
		}
		// Raw transport errors may contain a credential-bearing request URL.
		// Treat timeouts/network loss as unknown outcome, without logging or retries.
		return 0, errors.New("Telegram delivery outcome unknown")
	}
}
