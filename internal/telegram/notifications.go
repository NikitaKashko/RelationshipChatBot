package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
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
			text = "Свидание сохранено: " + strings.Join(p.Changed, ", ") + "\n" + p.Summary
			if strings.HasPrefix(p.Summary, "📝") {
				text = "Партнёр сохранил черновик свидания 💛\n" + p.Summary
			}
		case "plan_cancelled":
			text = "Свидание «" + p.Title + "» отменено. Идеи остаются в копилке."
		case "plan_not_went":
			text = fmt.Sprintf("Для свидания #%d отмечено «Не пошли». Места остаются доступными.", p.Plan)
		case "reminder":
			text = fmt.Sprintf("Скоро свидание 💛 План #%d — /plans", p.Plan)
		case "feedback":
			var visit int64
			if err := r.Store.DB.QueryRowContext(ctx, "SELECT coalesce(max(id),0) FROM visits WHERE plan_id=? AND pair_id=? AND valid=1", p.Plan, m.Pair).Scan(&visit); err != nil {
				return 0, notifications.ErrPermanent
			}
			if visit != 0 {
				text = fmt.Sprintf("Как тебе места на свидании? Факт посещения уже отмечен — осталось только твоё впечатление 💛\n/visitinfo %d", visit)
			} else {
				text = fmt.Sprintf("Как прошло свидание? 💛\nКогда захочешь, отметь посещённые места через /visit; если не пошли — /notwent %d. Отвечать сейчас необязательно, напоминать не буду.", p.Plan)
			}
		case "freeze_changed":
			text = fmt.Sprintf("У места #%d изменён общий фриз: %s. Ручной выбор доступен.", p.Place, p.Until)
			if p.Until == "" {
				text = fmt.Sprintf("У места #%d снят общий фриз. Личные предпочтения остаются.", p.Place)
			}
		case "visit_corrected":
			text = fmt.Sprintf("Исправлена ошибочная отметка посещения #%d. Автоматический фриз пересчитан, ручные настройки сохранены.", p.Visit)
		case "location_proposal":
			text = "Партнёр предложил город пары: " + p.City + " (" + p.Timezone + "). Открой /start, чтобы подтвердить."
		case "pair_created":
			text = "Вы теперь пара 💛 Открой /start, чтобы продолжить."
		case "affection", "budget_target", "partner_registered":
			text = p.Text
		default:
			return 0, notifications.ErrPermanent
		}
		// One outbox row sends one Telegram message, with no partial multi-send.
		runes := []rune(text)
		if len(runes) > 3500 {
			text = string(runes[:3500]) + "\nПолный план — /plans"
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
