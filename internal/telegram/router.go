package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Router struct {
	Store    *store.Store
	Username string
	AI       *ai.Service
}

func button(text, data string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: text, CallbackData: data}}}}
}
func (r *Router) send(ctx context.Context, b *bot.Bot, actor int64, text string, markup any) {
	// Never print raw Telegram transport errors: request URLs contain credentials.
	chars := []rune(text)
	for len(chars) > 2000 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: string(chars[:2000])})
		chars = chars[2000:]
	}
	text = string(chars)
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: text, ReplyMarkup: markup})
}

// sendAI renders the small Markdown subset DeepSeek commonly returns. Telegram
// rejects an entire formatted message on malformed Markdown, so delivery falls
// back to the original plain text instead of exposing a transport error.
func (r *Router) sendAI(ctx context.Context, b *bot.Bot, actor int64, text string, markup any) {
	formatted := strings.ReplaceAll(text, "**", "*")
	chars := []rune(formatted)
	for len(chars) > 2000 {
		chunk := string(chars[:2000])
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: chunk, ParseMode: models.ParseModeMarkdownV1}); err != nil {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: chunk})
		}
		chars = chars[2000:]
	}
	chunk := string(chars)
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: chunk, ParseMode: models.ParseModeMarkdownV1, ReplyMarkup: markup}); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: actor, Text: string(chars), ReplyMarkup: markup})
	}
}
func publicError(err error) string {
	if errors.Is(err, ErrAnswer) || errors.Is(err, ErrTimezone) {
		return err.Error()
	}
	if errors.Is(err, store.ErrDenied) {
		return texts.T("internal.telegram.router.001")
	}
	if errors.Is(err, store.ErrConflict) {
		return texts.T("internal.telegram.router.002")
	}
	return texts.T("internal.telegram.router.003")
}
func (r *Router) wizard(ctx context.Context, b *bot.Bot, actor int64, w Wizard) {
	var text string
	var markup any
	switch w.Stage {
	case "city":
		text = texts.T("internal.telegram.router.004")
	case "name":
		text = texts.T("internal.telegram.router.005")
	case "timezone":
		text = texts.T("internal.telegram.router.006")
		markup = button(texts.T("internal.telegram.router.007"), "timezone:moscow")
	case "preview":
		e := Enrollment{Store: r.Store}
		draft, err := e.ProfileDraft(ctx, actor, w)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		text = fmt.Sprintf(texts.T("internal.telegram.router.008"), w.Profile.City, w.Profile.Name, w.Profile.Timezone)
		markup = button(texts.T("internal.telegram.router.009"), "save:"+draft)
	}
	r.send(ctx, b, actor, text, markup)
}
func (r *Router) home(ctx context.Context, b *bot.Bot, actor int64) {
	_, err := store.UserFor(ctx, r.Store.DB, actor, false)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	pair, err := store.PairFor(ctx, r.Store.DB, actor)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	if pair != 0 {
		var proposedBy sql.NullInt64
		var city, zone sql.NullString
		var version int64
		if err = r.Store.DB.QueryRowContext(ctx, "SELECT proposed_by,proposed_city,proposed_timezone,location_version FROM pairs WHERE id=?", pair).Scan(&proposedBy, &city, &zone, &version); err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		if proposedBy.Valid && proposedBy.Int64 != actor {
			r.draft(ctx, b, actor, "location_confirm", identity.LocationConfirmation{Version: version}, texts.T("internal.telegram.router.010")+city.String+texts.T("internal.telegram.collection.030")+zone.String)
			return
		}
		r.showMenu(ctx, b, actor, true)
		return
	}
	e := Enrollment{Store: r.Store}
	hash, name, err := e.PendingPartner(ctx, actor)
	if err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	if hash != "" {
		raw, _ := json.Marshal(map[string]string{"hash": hash})
		svc := actions.Service{Store: r.Store}
		draft, err := svc.Prepare(ctx, actor, "pair_accept", raw)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.send(ctx, b, actor, texts.T("internal.telegram.router.011")+name+"?", button(texts.T("internal.telegram.router.012"), "save:"+draft))
		return
	}
	r.showMenu(ctx, b, actor, false)
}
func (r *Router) Handle(ctx context.Context, b *bot.Bot, update *models.Update) {
	if q := update.CallbackQuery; q != nil {
		if q.Message.Message == nil || q.Message.Message.Chat.Type != models.ChatTypePrivate || q.Message.Message.Chat.ID != q.From.ID {
			return
		}
		if err := r.syncTelegramName(ctx, q.From); err != nil {
			return
		}
		if q.Data == "timezone:moscow" {
			w, err := (&Enrollment{Store: r.Store}).Input(ctx, q.From.ID, "Europe/Moscow")
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, ShowAlert: err != nil, Text: func() string {
				if err != nil {
					return publicError(err)
				}
				return texts.T("internal.telegram.router.013")
			}()})
			if err == nil {
				r.wizard(ctx, b, q.From.ID, w)
			}
			return
		}
		if r.entryCallback(ctx, b, q) || r.menuCallback(ctx, b, q) || r.aiCallback(ctx, b, q) || r.affectionCallback(ctx, b, q) || r.feedbackCallback(ctx, b, q) || r.collectionCallback(ctx, b, q) {
			return
		}
		if !strings.HasPrefix(q.Data, "save:") {
			return
		}
		svc := actions.Service{Store: r.Store}
		result, err := svc.Apply(ctx, q.From.ID, strings.TrimPrefix(q.Data, "save:"))
		text := texts.T("internal.telegram.router.014")
		if err != nil {
			text = publicError(err)
		} else {
			var receipt struct {
				CollectionAdded bool `json:"collection_added"`
			}
			if json.Unmarshal(result, &receipt) == nil && receipt.CollectionAdded {
				text = texts.T("internal.telegram.router.015")
			}
		}
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text, ShowAlert: err != nil})
		if err == nil {
			r.saved(ctx, b, q.From.ID, result)
		} else {
			r.duplicate(ctx, b, q.From.ID, err)
			var stale *planning.Stale
			if errors.As(err, &stale) {
				r.send(ctx, b, q.From.ID, texts.T("internal.telegram.router.016"), nil)
				raw, _ := json.Marshal(map[string]int64{"plan": stale.ID})
				r.saved(ctx, b, q.From.ID, raw)
			}
		}
		return
	}
	m := update.Message
	if m == nil || m.From == nil || m.Chat.Type != models.ChatTypePrivate || m.Chat.ID != m.From.ID {
		return
	}
	actor := m.From.ID
	if err := r.syncTelegramName(ctx, *m.From); err != nil {
		r.send(ctx, b, actor, publicError(err), nil)
		return
	}
	text := strings.TrimSpace(m.Text)
	if command, ok := menuCommands[text]; ok {
		text = command
		copyMessage := *m
		copyMessage.Text = text
		m = &copyMessage
	}
	ids := identity.Service{Store: r.Store}
	e := Enrollment{Store: r.Store}
	parts := strings.Fields(text)
	if len(parts) > 0 && strings.Split(parts[0], "@")[0] == "/start" {
		if len(parts) > 1 {
			if _, err := ids.Claim(ctx, actor, telegramName(*m.From), parts[1]); err != nil {
				r.send(ctx, b, actor, publicError(err), nil)
				return
			}
		}
		u, err := store.UserFor(ctx, r.Store.DB, actor, true)
		if err != nil {
			r.send(ctx, b, actor, texts.T("internal.telegram.router.017"), nil)
			return
		}
		if u.Status == "ready" {
			if _, err := r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind IN ('ui_quiet','ui_entry')", actor); err != nil {
				r.send(ctx, b, actor, publicError(err), nil)
				return
			}
			r.home(ctx, b, actor)
			return
		}
		w, err := e.Begin(ctx, actor)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.wizard(ctx, b, actor, w)
		return
	}
	u, err := store.UserFor(ctx, r.Store.DB, actor, true)
	if err != nil {
		r.send(ctx, b, actor, texts.T("internal.telegram.router.018"), nil)
		return
	}
	if u.Status != "ready" {
		if text == "" {
			r.send(ctx, b, actor, texts.T("internal.telegram.router.019"), nil)
			return
		}
		if strings.HasPrefix(text, "/") && text != "/skip" {
			r.send(ctx, b, actor, texts.T("internal.telegram.router.020"), nil)
			return
		}
		w, err := e.Input(ctx, actor, text)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.wizard(ctx, b, actor, w)
		return
	}
	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "/quiet") {
		if _, err := r.Store.DB.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ui_quiet'", actor); err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
	}
	if r.entryMessage(ctx, b, actor, text) {
		return
	}
	if text == "/settings" {
		markup := navButtons(texts.T("internal.telegram.router.021"), "quiet", texts.T("internal.telegram.menu.004"), "ai", texts.T("internal.telegram.router.022"), "location", texts.T("internal.telegram.router.023"), "names", texts.T("internal.telegram.entry.004"), "candidates", texts.T("internal.telegram.router.024"), "cancelinvite", texts.T("internal.telegram.ai.017"), "start")
		markup.InlineKeyboard = append([][]models.InlineKeyboardButton{{{Text: texts.T("internal.telegram.menu.008"), CallbackData: "menu:accessinvite"}}, {{Text: texts.T("internal.telegram.router.025"), CallbackData: "menu:cancelaccessinvite"}}}, markup.InlineKeyboard...)
		r.send(ctx, b, actor, texts.T("internal.telegram.router.026"), markup)
		return
	}
	if text == "/accessinvite" {
		token, err := ids.Issue(ctx, actor, "access")
		if err != nil {
			if errors.Is(err, identity.ErrReferralUsed) {
				r.send(ctx, b, actor, texts.T("internal.telegram.router.027"), nil)
				return
			}
			if errors.Is(err, identity.ErrAccessInviteActive) {
				r.send(ctx, b, actor, texts.T("internal.telegram.router.028"), nil)
				return
			}
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		limit := texts.T("internal.telegram.router.029")
		if u.Role == "owner" {
			limit = texts.T("internal.telegram.router.030")
		}
		r.send(ctx, b, actor, texts.T("internal.telegram.router.031")+r.Username+"?start="+token+texts.T("internal.telegram.router.032")+limit, nil)
		return
	}
	if text == "/cancelaccessinvite" {
		if err := ids.CancelAccess(ctx, actor); err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
		} else {
			r.send(ctx, b, actor, texts.T("internal.telegram.router.033"), nil)
		}
		return
	}
	if r.quietMessage(ctx, b, actor, text) {
		return
	}
	if text == "/invite" {
		token, err := ids.Issue(ctx, actor, "partner")
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
			return
		}
		r.send(ctx, b, actor, texts.T("internal.telegram.router.034")+r.Username+"?start="+token+texts.T("internal.telegram.router.035"), nil)
		return
	}
	if m.Voice != nil {
		r.send(ctx, b, actor, texts.T("internal.telegram.router.036"), nil)
		return
	}
	if text == "/cancelinvite" {
		err := ids.CancelPartners(ctx, actor)
		if err != nil {
			r.send(ctx, b, actor, publicError(err), nil)
		} else {
			r.send(ctx, b, actor, texts.T("internal.telegram.router.037"), nil)
		}
		return
	}
	if r.collectionMessage(ctx, b, m) {
		return
	}
	if r.aiMessage(ctx, b, actor, text) {
		return
	}
	r.send(ctx, b, actor, texts.T("internal.telegram.router.038"), nil)
}
