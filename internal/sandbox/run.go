package sandbox

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/config"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/telegram"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func validate(c config.Config) error {
	if c.TestOwnerID <= 2 || c.TestBotToken == "" || c.TestBotToken == c.BotToken {
		return errors.New("separate test token and TEST_OWNER_ID required")
	}
	test, e := filepath.Abs(c.TestDatabasePath)
	if e != nil {
		return e
	}
	prod, e := filepath.Abs(c.DatabasePath)
	if e != nil {
		return e
	}
	if strings.EqualFold(test, prod) || filepath.Base(test) != "sandbox.sqlite" {
		return errors.New("test database must be separate sandbox.sqlite")
	}
	if stat, e := os.Lstat(test); e == nil && stat.Mode()&os.ModeSymlink != 0 {
		return errors.New("test database symlink denied")
	}
	if a, e := os.Stat(test); e == nil {
		if b, e := os.Stat(prod); e == nil && os.SameFile(a, b) {
			return errors.New("test database must not share production file")
		}
	}
	return nil
}

// Seed rejects populated unmarked databases before doing any domain writes.
func seed(ctx context.Context, db *store.Store, owner int64) error {
	var marker int
	if err := db.DB.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='sandbox_identity'").Scan(&marker); err != nil {
		return err
	}
	if marker == 1 {
		var saved int64
		if err := db.DB.QueryRowContext(ctx, "SELECT owner_id FROM sandbox_identity WHERE id=1").Scan(&saved); err != nil {
			return err
		}
		if saved != owner {
			return errors.New("test database belongs to another owner")
		}
		return nil
	}
	var count int
	if err := db.DB.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("unmarked nonempty test database denied")
	}
	ids := identity.Service{Store: db}
	token, err := ids.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if _, err = ids.Claim(ctx, 1, "Участник А", token); err != nil {
		return err
	}
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 1, identity.Profile{City: "Санкт-Петербург", Timezone: "Europe/Moscow"})
	}); err != nil {
		return err
	}
	token, err = ids.Issue(ctx, 1, "partner")
	if err != nil {
		return err
	}
	if _, err = ids.Claim(ctx, 2, "Тестовый партнёр", token); err != nil {
		return err
	}
	if err = db.Write(ctx, func(q store.Query) error {
		return ids.FinishProfile(ctx, q, 2, identity.Profile{City: "Санкт-Петербург", Timezone: "Europe/Moscow"})
	}); err != nil {
		return err
	}
	pair, err := ids.Accept(ctx, 2, token)
	if err != nil {
		return err
	}
	return db.Write(ctx, func(q store.Query) error {
		if _, err := q.ExecContext(ctx, "UPDATE pairs SET city='Санкт-Петербург',timezone='Europe/Moscow' WHERE id=?", pair); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM notifications"); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "CREATE TABLE sandbox_identity(id INTEGER PRIMARY KEY CHECK(id=1),owner_id INTEGER NOT NULL)"); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "INSERT INTO sandbox_identity VALUES(1,?)", owner)
		return err
	})
}

type simulator struct {
	owner int64
	role  atomic.Int64
}

func (s *simulator) mapUpdate(u *models.Update) (int64, bool) {
	role := s.role.Load()
	if role != 2 {
		role = 1
	}
	if q := u.CallbackQuery; q != nil {
		if q.From.ID != s.owner || q.Message.Message == nil || q.Message.Message.Chat.ID != s.owner || q.Message.Message.Chat.Type != models.ChatTypePrivate {
			return 0, false
		}
		switch {
		case strings.HasPrefix(q.Data, "sim:1:"):
			role = 1
			q.Data = strings.TrimPrefix(q.Data, "sim:1:")
		case strings.HasPrefix(q.Data, "sim:2:"):
			role = 2
			q.Data = strings.TrimPrefix(q.Data, "sim:2:")
		default:
			return 0, false
		}
		q.From = virtual(role, q.From)
		q.Message.Message.Chat.ID = role
	} else if m := u.Message; m != nil {
		if m.From == nil || m.From.ID != s.owner || m.Chat.ID != s.owner || m.Chat.Type != models.ChatTypePrivate {
			return 0, false
		}
		switch m.Text {
		case "/role_a", "🧪 Роль А":
			role = 1
			m.Text = "/start"
		case "/role_b", "🧪 Роль Б":
			role = 2
			m.Text = "/start"
		case "/start":
			m.Text = "/start"
		}
		v := virtual(role, *m.From)
		m.From = &v
		m.Chat.ID = role
	} else {
		return 0, false
	}
	s.role.Store(role)
	return role, true
}

func virtual(role int64, u models.User) models.User {
	u.ID = role
	u.Username = ""
	if role == 2 {
		u.LastName = ""
		u.FirstName = "Тестовый партнёр"
	}
	return u
}

func Run(c config.Config) error {
	if err := validate(c); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	init, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := store.Open(init, c.TestDatabasePath)
	if err != nil {
		return errors.New("test database initialization failed")
	}
	defer db.DB.Close()
	if err = seed(init, db, c.TestOwnerID); err != nil {
		return err
	}
	r := &telegram.Router{Store: db, AI: &ai.Service{Store: db, Model: c.DeepSeekModel}}
	if c.DeepSeekKey != "" {
		r.AI.Provider = &ai.DeepSeek{Key: c.DeepSeekKey, Model: c.DeepSeekModel}
		r.AI.Search = &ai.DeepSeekSearch{Key: c.DeepSeekKey, Model: c.DeepSeekModel}
	}
	s := &simulator{owner: c.TestOwnerID}
	s.role.Store(1)
	client := &http.Client{Timeout: 65 * time.Second, Transport: transport{owner: c.TestOwnerID, next: http.DefaultTransport}}
	b, err := bot.New(c.TestBotToken, bot.WithSkipGetMe(), bot.WithWorkers(1), bot.WithHTTPClient(60*time.Second, client), bot.WithErrorsHandler(func(error) {}), bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, u *models.Update) {
		if _, ok := s.mapUpdate(u); ok {
			r.Handle(ctx, b, u)
		}
	}))
	if err != nil {
		return errors.New("invalid test Telegram configuration")
	}
	me, err := b.GetMe(init)
	if err != nil {
		return errors.New("test Telegram initialization failed")
	}
	if strings.EqualFold(me.Username, c.BotUsername) {
		return errors.New("test bot must differ from production bot")
	}
	r.Username = me.Username
	if err = r.AI.Prune(init); err != nil {
		return err
	}
	worker := notifications.Worker{Store: db, Send: r.NotificationSender(b)}
	if err = worker.Recover(init); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { err := worker.Run(ctx); done <- err; stop() }()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if r.AI.Prune(ctx) != nil {
					stop()
					return
				}
			}
		}
	}()
	b.Start(ctx)
	stop()
	<-cleanupDone
	return <-done
}
