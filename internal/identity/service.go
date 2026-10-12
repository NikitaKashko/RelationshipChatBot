// Package identity provides authorized domain actions shared by buttons and AI.
// Actor must come from the authenticated transport, never model arguments.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type Service struct{ Store *store.Store }
type Invitation struct {
	Hash, Kind        string
	Sender, Claimed   sql.NullInt64
	Expires           float64
	Revoked, Consumed sql.NullFloat64
}

func RandomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func Hash(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }

func (s *Service) issue(ctx context.Context, q store.Query, sender any, kind string) (string, error) {
	token, err := RandomToken()
	if err != nil {
		return "", err
	}
	now := s.Store.Now().Unix()
	_, err = q.ExecContext(ctx, "INSERT INTO invitations(token_hash,kind,sender_id,created_at,expires_at) VALUES(?,?,?,?,?)", Hash(token), kind, sender, now, now+7*86400)
	return token, err
}

func (s *Service) Bootstrap(ctx context.Context) (token string, err error) {
	err = s.Store.Write(ctx, func(q store.Query) error {
		var count int
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE role='owner' AND status<>'deleted'").Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return store.ErrConflict
		}
		if _, err := q.ExecContext(ctx, "UPDATE invitations SET revoked_at=? WHERE kind='owner' AND consumed_at IS NULL", s.Store.Now().Unix()); err != nil {
			return err
		}
		var e error
		token, e = s.issue(ctx, q, nil, "owner")
		return e
	})
	return
}

func (s *Service) Issue(ctx context.Context, actor int64, kind string) (token string, err error) {
	err = s.Store.Write(ctx, func(q store.Query) error {
		u, e := store.UserFor(ctx, q, actor, false)
		if e != nil {
			return e
		}
		switch kind {
		case "access":
			if u.Role != "owner" {
				return store.ErrDenied
			}
		case "partner":
			p, e := store.PairFor(ctx, q, actor)
			if e != nil {
				return e
			}
			if p != 0 {
				return store.ErrConflict
			}
		default:
			return errors.New("неизвестный вид приглашения")
		}
		token, e = s.issue(ctx, q, actor, kind)
		return e
	})
	return
}

func (s *Service) valid(ctx context.Context, q store.Query, actor int64, hash string) (Invitation, error) {
	var i Invitation
	err := q.QueryRowContext(ctx, "SELECT token_hash,kind,sender_id,claimed_by,expires_at,revoked_at,consumed_at FROM invitations WHERE token_hash=?", hash).Scan(&i.Hash, &i.Kind, &i.Sender, &i.Claimed, &i.Expires, &i.Revoked, &i.Consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return i, store.ErrConflict
	}
	if err != nil {
		return i, err
	}
	if i.Revoked.Valid || i.Consumed.Valid || i.Expires <= float64(s.Store.Now().Unix()) {
		return i, store.ErrConflict
	}
	if i.Claimed.Valid && i.Claimed.Int64 != actor {
		return i, store.ErrDenied
	}
	if i.Kind == "partner" {
		if i.Sender.Int64 == actor {
			return i, store.ErrConflict
		}
		if _, err = store.UserFor(ctx, q, i.Sender.Int64, false); err != nil {
			return i, err
		}
		p, e := store.PairFor(ctx, q, i.Sender.Int64)
		if e != nil {
			return i, e
		}
		if p != 0 {
			return i, store.ErrConflict
		}
	}
	return i, nil
}

func (s *Service) Claim(ctx context.Context, actor int64, name, token string) (kind string, err error) {
	err = s.Store.Write(ctx, func(q store.Query) error {
		i, e := s.valid(ctx, q, actor, Hash(token))
		if e != nil {
			return e
		}
		if i.Kind == "partner" {
			p, e := store.PairFor(ctx, q, actor)
			if e != nil {
				return e
			}
			if p != 0 {
				return store.ErrConflict
			}
		}
		var status string
		e = q.QueryRowContext(ctx, "SELECT status FROM users WHERE id=?", actor).Scan(&status)
		if errors.Is(e, sql.ErrNoRows) {
			code, e := RandomToken()
			if e != nil {
				return e
			}
			role := "member"
			if i.Kind == "owner" {
				role = "owner"
			}
			_, e = q.ExecContext(ctx, "INSERT INTO users(id,name,code,notification_name,role,created_at) VALUES(?,?,?,?,?,?)", actor, name, strings.ToUpper(code[:10]), name, role, s.Store.Now().Unix())
			if e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else if status == "deleted" {
			return store.ErrDenied
		}
		if i.Kind == "owner" {
			var owners int
			if e = q.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE role='owner' AND id<>? AND status<>'deleted'", actor).Scan(&owners); e != nil {
				return e
			}
			if owners > 0 {
				return store.ErrConflict
			}
			if _, e = q.ExecContext(ctx, "UPDATE users SET role='owner' WHERE id=?", actor); e != nil {
				return e
			}
		}
		_, e = q.ExecContext(ctx, "UPDATE invitations SET claimed_by=? WHERE token_hash=?", actor, i.Hash)
		kind = i.Kind
		return e
	})
	return
}

func (s *Service) Revoke(ctx context.Context, actor int64, token string) error {
	return s.RevokeHash(ctx, actor, Hash(token))
}

func (s *Service) CancelPartners(ctx context.Context, actor int64) error {
	return s.Store.Write(ctx, func(q store.Query) error {
		if _, err := store.UserFor(ctx, q, actor, false); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "UPDATE invitations SET revoked_at=? WHERE sender_id=? AND kind='partner' AND consumed_at IS NULL AND revoked_at IS NULL", s.Store.Now().Unix(), actor)
		return err
	})
}

func (s *Service) RevokeHash(ctx context.Context, actor int64, hash string) error {
	return s.Store.Write(ctx, func(q store.Query) error {
		u, e := store.UserFor(ctx, q, actor, false)
		if e != nil {
			return e
		}
		var sender sql.NullInt64
		e = q.QueryRowContext(ctx, "SELECT sender_id FROM invitations WHERE token_hash=?", hash).Scan(&sender)
		if errors.Is(e, sql.ErrNoRows) {
			return store.ErrDenied
		}
		if e != nil {
			return e
		}
		if u.Role != "owner" && (!sender.Valid || sender.Int64 != actor) {
			return store.ErrDenied
		}
		_, e = q.ExecContext(ctx, "UPDATE invitations SET revoked_at=? WHERE token_hash=?", s.Store.Now().Unix(), hash)
		return e
	})
}

type Profile struct {
	City     string `json:"city"`
	Timezone string `json:"timezone"`
	Name     string `json:"name"`
}

func (s *Service) FinishProfile(ctx context.Context, q store.Query, actor int64, p Profile) error {
	if strings.TrimSpace(p.City) == "" {
		return errors.New("укажи город")
	}
	if p.Timezone == "" || p.Timezone == "Local" {
		return errors.New("неизвестный часовой пояс")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return errors.New("неизвестный часовой пояс")
	}
	u, err := store.UserFor(ctx, q, actor, true)
	if err != nil {
		return err
	}
	if u.Status != "ready" {
		var pending int
		if err = q.QueryRowContext(ctx, "SELECT count(*) FROM invitations WHERE claimed_by=? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?", actor, s.Store.Now().Unix()).Scan(&pending); err != nil {
			return err
		}
		if pending == 0 {
			var legacy int
			if err = q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='legacy_users'").Scan(&legacy); err != nil {
				return err
			}
			if legacy == 0 {
				return store.ErrDenied
			}
			if err = q.QueryRowContext(ctx, "SELECT count(*) FROM legacy_users WHERE id=?", actor).Scan(&legacy); err != nil {
				return err
			}
			if legacy == 0 {
				return store.ErrDenied
			}
		}
	}
	// The authenticated Telegram display name is authoritative; profile drafts
	// cannot choose an outgoing signature, including drafts from older versions.
	p.Name = u.Name
	if _, err = q.ExecContext(ctx, "UPDATE users SET city=?,timezone=?,notification_name=?,status='ready' WHERE id=?", strings.TrimSpace(p.City), p.Timezone, strings.TrimSpace(p.Name), actor); err != nil {
		return err
	}
	if u.Status != "ready" {
		rows, e := q.QueryContext(ctx, "SELECT token_hash,sender_id FROM invitations WHERE claimed_by=? AND kind='partner' AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?", actor, s.Store.Now().Unix())
		if e != nil {
			return e
		}
		type invite struct {
			hash   string
			sender int64
		}
		pending := []invite{}
		for rows.Next() {
			var i invite
			if e = rows.Scan(&i.hash, &i.sender); e != nil {
				rows.Close()
				return e
			}
			pending = append(pending, i)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, i := range pending {
			payload, _ := json.Marshal(map[string]string{"text": strings.TrimSpace(p.Name) + " теперь в боте 💛 Регистрация готова — осталось подтвердить создание пары.", "invitation_hash": i.hash})
			_, e = q.ExecContext(ctx, "INSERT INTO notifications(semantic_key,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,'partner_registered',?,?,?,?) ON CONFLICT(semantic_key) DO NOTHING", "invitation:"+i.hash+":registered", i.sender, string(payload), s.Store.Now().Unix(), s.Store.Now().Unix()+48*3600, s.Store.Now().Unix())
			if e != nil {
				return e
			}
		}
	}
	_, err = q.ExecContext(ctx, "UPDATE invitations SET consumed_at=? WHERE claimed_by=? AND kind IN ('owner','access') AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?", s.Store.Now().Unix(), actor, s.Store.Now().Unix())
	return err
}

func (s *Service) Accept(ctx context.Context, actor int64, token string) (pair int64, err error) {
	err = s.Store.Write(ctx, func(q store.Query) error { var e error; pair, e = s.AcceptIn(ctx, q, actor, Hash(token)); return e })
	return
}

func (s *Service) AcceptIn(ctx context.Context, q store.Query, actor int64, hash string) (int64, error) {
	if _, err := store.UserFor(ctx, q, actor, false); err != nil {
		return 0, err
	}
	i, err := s.valid(ctx, q, actor, hash)
	if err != nil {
		return 0, err
	}
	if i.Kind != "partner" || !i.Claimed.Valid || i.Claimed.Int64 != actor {
		return 0, store.ErrDenied
	}
	p, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	if p != 0 {
		return 0, store.ErrConflict
	}
	r, err := q.ExecContext(ctx, "INSERT INTO pairs(created_at) VALUES(?)", s.Store.Now().Unix())
	if err != nil {
		return 0, err
	}
	pair, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, uid := range []int64{actor, i.Sender.Int64} {
		if _, err = q.ExecContext(ctx, "INSERT INTO pair_members(pair_id,user_id) VALUES(?,?)", pair, uid); err != nil {
			return 0, err
		}
		// Copy category names into the pair scope before moving personal cards.
		scope := fmt.Sprintf("pair:%d", pair)
		if _, err = q.ExecContext(ctx, `INSERT OR IGNORE INTO categories(scope,title) SELECT ?,c.title FROM places p JOIN place_categories pc ON pc.place_id=p.id JOIN categories c ON c.id=pc.category_id WHERE p.personal_user_id=? AND p.archived=0`, scope, uid); err != nil {
			return 0, err
		}
		if _, err = q.ExecContext(ctx, `UPDATE place_categories SET category_id=(SELECT c2.id FROM categories c1 JOIN categories c2 ON c2.title=c1.title AND c2.scope=? WHERE c1.id=place_categories.category_id) WHERE place_id IN (SELECT id FROM places WHERE personal_user_id=? AND archived=0)`, scope, uid); err != nil {
			return 0, err
		}
		if _, err = q.ExecContext(ctx, "UPDATE places SET pair_id=?,personal_user_id=NULL WHERE personal_user_id=? AND archived=0", pair, uid); err != nil {
			return 0, err
		}
	}
	if _, err = q.ExecContext(ctx, "UPDATE invitations SET consumed_at=? WHERE token_hash=?", s.Store.Now().Unix(), i.Hash); err != nil {
		return 0, err
	}
	if _, err = q.ExecContext(ctx, "UPDATE invitations SET revoked_at=? WHERE kind='partner' AND consumed_at IS NULL AND (sender_id IN (?,?) OR claimed_by IN (?,?))", s.Store.Now().Unix(), actor, i.Sender.Int64, actor, i.Sender.Int64); err != nil {
		return 0, err
	}
	u, err := store.UserFor(ctx, q, actor, false)
	if err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]string{"partner_name": u.NotificationName})
	_, err = q.ExecContext(ctx, `INSERT INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,?,'pair_created',?,?,?,?)`, fmt.Sprintf("pair:%d:created", pair), pair, i.Sender.Int64, string(payload), s.Store.Now().Unix(), s.Store.Now().Unix()+48*3600, s.Store.Now().Unix())
	return pair, err
}
