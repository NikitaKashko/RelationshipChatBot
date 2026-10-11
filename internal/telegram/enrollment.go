// Package telegram maps authenticated private updates to domain services.
package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/actions"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type Enrollment struct{ Store *store.Store }

var ErrAnswer = errors.New("Ответ должен содержать от 1 до 100 символов.")
var ErrTimezone = errors.New("Не распознал часовой пояс. Для Москвы и Санкт-Петербурга напиши «Москва» или нажми «Московское время» после /start. Для другого региона можно указать, например, Asia/Yekaterinburg.")

func normalizeTimezone(text string) (string, error) {
	aliases := map[string]string{
		"москва": "Europe/Moscow", "moscow": "Europe/Moscow", "moskow": "Europe/Moscow",
		"europe/moscow": "Europe/Moscow", "europe/moskow": "Europe/Moscow",
		"московское время": "Europe/Moscow", "санкт-петербург": "Europe/Moscow", "спб": "Europe/Moscow",
		"калининград": "Europe/Kaliningrad", "екатеринбург": "Asia/Yekaterinburg",
		"новосибирск": "Asia/Novosibirsk", "владивосток": "Asia/Vladivostok",
		"utc": "UTC",
	}
	text = strings.TrimSpace(text)
	if zone, ok := aliases[strings.ToLower(text)]; ok {
		text = zone
	}
	if text == "Local" {
		return "", ErrTimezone
	}
	if _, err := time.LoadLocation(text); err != nil {
		return "", ErrTimezone
	}
	return text, nil
}

type Wizard struct {
	Stage   string           `json:"stage"`
	Profile identity.Profile `json:"profile"`
}

func (e *Enrollment) Begin(ctx context.Context, actor int64) (Wizard, error) {
	var w Wizard
	err := e.Store.Write(ctx, func(q store.Query) error {
		u, err := store.UserFor(ctx, q, actor, true)
		if err != nil {
			return err
		}
		if u.Status == "ready" {
			return store.ErrConflict
		}
		var payload string
		err = q.QueryRowContext(ctx, "SELECT payload FROM drafts WHERE user_id=? AND kind='onboarding' AND applied_at IS NULL AND expires_at>? ORDER BY created_at DESC LIMIT 1", actor, e.Store.Now().Unix()).Scan(&payload)
		if err == nil {
			return json.Unmarshal([]byte(payload), &w)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		w = Wizard{Stage: "city", Profile: identity.Profile{Name: u.NotificationName}}
		return e.save(ctx, q, actor, w)
	})
	return w, err
}

func (e *Enrollment) save(ctx context.Context, q store.Query, actor int64, w Wizard) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='onboarding'", actor); err != nil {
		return err
	}
	id, err := identity.RandomToken()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO drafts(id,user_id,kind,payload,created_at,expires_at) VALUES(?,?,'onboarding',?,?,?)", id, actor, string(payload), e.Store.Now().Unix(), e.Store.Now().Unix()+7*86400)
	return err
}

func (e *Enrollment) Input(ctx context.Context, actor int64, text string) (Wizard, error) {
	var w Wizard
	text = strings.TrimSpace(text)
	if len([]rune(text)) == 0 || len([]rune(text)) > 100 {
		return w, ErrAnswer
	}
	err := e.Store.Write(ctx, func(q store.Query) error {
		if _, err := store.UserFor(ctx, q, actor, true); err != nil {
			return err
		}
		var payload string
		err := q.QueryRowContext(ctx, "SELECT payload FROM drafts WHERE user_id=? AND kind='onboarding' AND applied_at IS NULL AND expires_at>? ORDER BY created_at DESC LIMIT 1", actor, e.Store.Now().Unix()).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrConflict
		}
		if err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(payload), &w); err != nil {
			return err
		}
		switch w.Stage {
		case "city":
			w.Profile.City = text
			w.Stage = "name"
		case "name":
			if text != "/skip" {
				w.Profile.Name = text
			}
			w.Stage = "timezone"
		case "timezone":
			zone, err := normalizeTimezone(text)
			if err != nil {
				return err
			}
			w.Profile.Timezone = zone
			w.Stage = "preview"
		default:
			return store.ErrConflict
		}
		return e.save(ctx, q, actor, w)
	})
	return w, err
}

func (e *Enrollment) ProfileDraft(ctx context.Context, actor int64, w Wizard) (string, error) {
	if w.Stage != "preview" {
		return "", store.ErrConflict
	}
	raw, err := json.Marshal(w.Profile)
	if err != nil {
		return "", err
	}
	a := actions.Service{Store: e.Store}
	return a.Prepare(ctx, actor, "profile", raw)
}

func (e *Enrollment) PendingPartner(ctx context.Context, actor int64) (hash, name string, err error) {
	err = e.Store.DB.QueryRowContext(ctx, `SELECT i.token_hash,u.notification_name FROM invitations i JOIN users u ON u.id=i.sender_id WHERE i.kind='partner' AND i.claimed_by=? AND i.revoked_at IS NULL AND i.consumed_at IS NULL AND i.expires_at>? AND u.status='ready' AND NOT EXISTS(SELECT 1 FROM pair_members m WHERE m.user_id=u.id AND m.active=1) ORDER BY i.created_at DESC LIMIT 1`, actor, e.Store.Now().Unix()).Scan(&hash, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return
}
