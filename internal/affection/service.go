// Package affection handles only explicit clicks and never calls an AI provider.
package affection

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

var ErrCooldown = errors.New("подожди минутку перед следующим знаком внимания")
var phrases = map[string][]string{
	"attention": {"%s хочет твоего внимания 💛", "%s скучает по твоему вниманию 💛", "%s ждёт немного твоего тепла 💛", "%s хочет побыть с тобой 💛", "%s ждёт твоего сообщения 💛", "%s мечтает почувствовать тебя рядом 💛"},
	"thinking":  {"%s думает о тебе 💭", "%s снова вспоминает тебя 💛", "%s вспоминает ваши моменты 💛", "%s мысленно рядом с тобой 💭", "%s улыбается, думая о тебе 💛", "%s передаёт тебе тёплую мысль 💭"},
	"hug":       {"%s хочет тебя обнять 🤗", "%s отправляет тебе мысленные объятия 🤗", "%s хочет прижаться к тебе 💛", "%s ждёт ваших следующих объятий 🤗", "%s хочет согреть тебя объятием 💛", "%s передаёт тебе крепкое объятие 🤗"},
}

type Service struct{ Store *store.Store }
type Names struct {
	Partner string `json:"partner"`
}

func (s *Service) Names(ctx context.Context, q store.Query, actor int64, p Names) error {
	if _, err := store.UserFor(ctx, q, actor, false); err != nil {
		return err
	}
	p.Partner = strings.TrimSpace(p.Partner)
	if len([]rune(p.Partner)) > 60 || strings.ContainsAny(p.Partner, "\r\n") {
		return errors.New("имя должно быть коротким и в одну строку")
	}
	_, err := q.ExecContext(ctx, "UPDATE users SET partner_nickname=? WHERE id=?", p.Partner, actor)
	return err
}

func (s *Service) Click(ctx context.Context, actor int64, kind string) error {
	options, ok := phrases[kind]
	if !ok {
		return store.ErrDenied
	}
	return s.Store.Write(ctx, func(q store.Query) error {
		u, err := store.UserFor(ctx, q, actor, false)
		if err != nil {
			return err
		}
		pair, err := store.PairFor(ctx, q, actor)
		if err != nil {
			return err
		}
		if pair == 0 {
			return store.ErrDenied
		}
		var last sql.NullFloat64
		if err = q.QueryRowContext(ctx, "SELECT max(at) FROM cooldowns WHERE user_id=? AND kind='affection'", actor).Scan(&last); err != nil {
			return err
		}
		now := s.Store.Now()
		if last.Valid && float64(now.Unix())-last.Float64 < 60 {
			return ErrCooldown
		}
		var recipient int64
		var nickname string
		if err = q.QueryRowContext(ctx, "SELECT u.id,coalesce(u.partner_nickname,'') FROM pair_members m JOIN users u ON u.id=m.user_id WHERE m.pair_id=? AND m.active=1 AND m.user_id<>? AND u.status='ready'", pair, actor).Scan(&recipient, &nickname); err != nil {
			return err
		}
		name := u.Name
		if strings.TrimSpace(nickname) != "" {
			name = nickname
		}
		var previous sql.NullInt64
		err = q.QueryRowContext(ctx, "SELECT last_phrase FROM cooldowns WHERE user_id=? AND kind=?", actor, kind).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		choices := []int{}
		for i := range options {
			if !previous.Valid || int64(i) != previous.Int64 {
				choices = append(choices, i)
			}
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(choices))))
		if err != nil {
			return err
		}
		index := choices[n.Int64()]
		for _, key := range []string{"affection", kind} {
			if _, err = q.ExecContext(ctx, `INSERT INTO cooldowns(user_id,kind,at,last_phrase) VALUES(?,?,?,?) ON CONFLICT(user_id,kind) DO UPDATE SET at=excluded.at,last_phrase=excluded.last_phrase`, actor, key, now.Unix(), index); err != nil {
				return err
			}
		}
		payload, _ := json.Marshal(map[string]string{"text": fmt.Sprintf(options[index], name)})
		_, err = q.ExecContext(ctx, `INSERT INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,?,'affection',?,?,?,?)`, fmt.Sprintf("affection:%d:%d", actor, now.Unix()), pair, recipient, string(payload), now.Unix(), now.Add(12*time.Hour).Unix(), now.Unix())
		return err
	})
}
