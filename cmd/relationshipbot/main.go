package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/backup"
	"github.com/NikitaKashko/RelationshipChatBot/internal/config"
	"github.com/NikitaKashko/RelationshipChatBot/internal/dashboard"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/sandbox"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/telegram"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	command := "check"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch command {
	case "serve-test":
		return sandbox.Run(cfg)
	case "check-test":
		if cfg.TestBotToken == "" || cfg.TestBotToken == cfg.BotToken {
			return fmt.Errorf("separate TEST_BOT_TOKEN required")
		}
		b, e := bot.New(cfg.TestBotToken, bot.WithSkipGetMe(), bot.WithErrorsHandler(func(error) {}))
		if e != nil {
			return fmt.Errorf("invalid test configuration")
		}
		me, e := b.GetMe(ctx)
		if e != nil {
			return fmt.Errorf("test Telegram credential check failed")
		}
		if strings.EqualFold(me.Username, cfg.BotUsername) {
			return fmt.Errorf("test bot matches production bot")
		}
		if cfg.BotToken != "" {
			prod, e := bot.New(cfg.BotToken, bot.WithSkipGetMe(), bot.WithErrorsHandler(func(error) {}))
			if e != nil {
				return fmt.Errorf("invalid production configuration")
			}
			p, e := prod.GetMe(ctx)
			if e != nil {
				return fmt.Errorf("production identity check failed")
			}
			if p.ID == me.ID {
				return fmt.Errorf("test bot matches production bot")
			}
		}
		fmt.Printf("TEST_BOT_OK @%s\n", me.Username)
		return nil
	case "owner-id":
		db, err := store.Open(ctx, cfg.DatabasePath)
		if err != nil {
			return fmt.Errorf("database initialization failed")
		}
		defer db.DB.Close()
		var id int64
		if err = db.DB.QueryRowContext(ctx, "SELECT id FROM users WHERE role='owner' AND status='ready'").Scan(&id); err != nil {
			return fmt.Errorf("registered owner missing")
		}
		fmt.Println(id)
		return nil
	case "set-avatar":
		if len(os.Args) != 3 {
			return fmt.Errorf("set-avatar requires image path")
		}
		f, err := os.Open(os.Args[2])
		if err != nil {
			return fmt.Errorf("cannot open avatar")
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil {
			return fmt.Errorf("invalid avatar image")
		}
		var jpegData bytes.Buffer
		if err = jpeg.Encode(&jpegData, img, &jpeg.Options{Quality: 95}); err != nil {
			return fmt.Errorf("cannot encode avatar")
		}
		b, err := bot.New(cfg.BotToken, bot.WithSkipGetMe(), bot.WithErrorsHandler(func(error) {}))
		if err != nil {
			return fmt.Errorf("invalid Telegram configuration")
		}
		ok, err := b.SetMyProfilePhoto(ctx, &bot.SetMyProfilePhotoParams{Photo: &models.InputProfilePhotoStatic{Photo: "attach://avatar.jpg", MediaAttachment: bytes.NewReader(jpegData.Bytes())}})
		if err != nil || !ok {
			return fmt.Errorf("Telegram avatar update failed")
		}
		fmt.Println("AVATAR_UPDATED")
		return nil
	case "panel", "backup", "verify-backup":
		appCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if command == "verify-backup" {
			if len(os.Args) != 3 {
				return fmt.Errorf("verify-backup requires snapshot path")
			}
			if err := backup.Verify(appCtx, os.Args[2]); err != nil {
				return err
			}
			fmt.Println("BACKUP_INTEGRITY_OK")
			return nil
		}
		db, err := store.Open(ctx, cfg.DatabasePath)
		if err != nil {
			return fmt.Errorf("database initialization failed")
		}
		defer db.DB.Close()
		backups := &backup.Service{Store: db, Dir: cfg.BackupDir}
		if command == "backup" {
			job, cancel := context.WithTimeout(appCtx, 2*time.Minute)
			defer cancel()
			if _, err = backups.Run(job); err != nil {
				return fmt.Errorf("backup failed: %w", err)
			}
			fmt.Println("BACKUP_OK verified consistent snapshot")
			return nil
		}
		panel := &dashboard.Panel{Store: db, Backup: backups, Password: cfg.PanelPassword, Username: cfg.BotUsername, HealthPath: filepath.Join(filepath.Dir(cfg.DatabasePath), "heartbeat")}
		return panel.Serve(appCtx)
	case "check-search":
		searchCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		query := "Найди 3 новых кафе для свидания в центре Москвы. Нужны реальные названия, адреса и источники; неизвестные цены не придумывай."
		mode := "discover"
		if len(os.Args) > 2 {
			query = os.Args[2]
			mode = "identify"
		}
		provider := ai.DeepSeekSearch{Key: cfg.DeepSeekKey, Model: cfg.DeepSeekModel}
		found, err := provider.Search(searchCtx, ai.SearchRequest{Query: query, City: "Москва", Mode: mode})
		if err != nil {
			return fmt.Errorf("native search check failed: %s", err.Error())
		}
		fmt.Printf("SEARCH_OK grounded_cards=%d (synthetic/public request; no private collection)\n", len(found.Places))
		for _, place := range found.Places {
			fmt.Printf("%s | %s | sources=%d | unknown=%d\n", place.Card.Title, place.Card.Address, len(place.Card.Sources), len(place.Unknown))
			for _, source := range place.Card.Sources {
				fmt.Println(source.URL)
			}
		}
		if found.Usage != nil {
			fmt.Printf("usage input=%d cached=%d output=%d\n", found.Usage.Input, found.Usage.Cached, found.Usage.Output)
		}
		if found.Requests != nil {
			fmt.Printf("search_attempts=%d (not guaranteed successful searches)\n", *found.Requests)
		}
		return nil
	case "check-ai-flow":
		flowCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return checkAIFlow(flowCtx, cfg)
	case "check-ai":
		provider := ai.DeepSeek{Key: cfg.DeepSeekKey, Model: cfg.DeepSeekModel}
		answer, err := provider.Complete(ctx, []ai.Message{{Role: "user", Content: "Ответь одним словом: OK"}}, nil)
		if err != nil {
			return fmt.Errorf("DeepSeek credential check failed")
		}
		if answer.Message.Content == "" {
			return fmt.Errorf("DeepSeek returned no text")
		}
		fmt.Println("AI_OK (synthetic request; no personal data)")
		return nil
	case "serve":
		if cfg.BotToken == "" {
			return fmt.Errorf("BOT_TOKEN is missing")
		}
		db, err := store.Open(ctx, cfg.DatabasePath)
		if err != nil {
			return fmt.Errorf("database initialization failed")
		}
		defer db.DB.Close()
		r := &telegram.Router{Store: db}
		r.AI = &ai.Service{Store: db, Model: cfg.DeepSeekModel, Billing: ai.Billing{USDRUB: cfg.USDRUB, TargetRUB: cfg.BudgetTargetRUB}}
		if cfg.DeepSeekKey != "" {
			r.AI.Provider = &ai.DeepSeek{Key: cfg.DeepSeekKey, Model: cfg.DeepSeekModel}
			r.AI.Search = &ai.DeepSeekSearch{Key: cfg.DeepSeekKey, Model: cfg.DeepSeekModel}
		}
		// Retention/clear remain active when the external provider is disabled.
		if err = r.AI.Prune(ctx); err != nil {
			return fmt.Errorf("AI history cleanup failed")
		}
		b, err := bot.New(cfg.BotToken, bot.WithSkipGetMe(), bot.WithErrorsHandler(func(error) {}), bot.WithDefaultHandler(r.Handle))
		if err != nil {
			return fmt.Errorf("invalid Telegram configuration")
		}
		me, err := b.GetMe(ctx)
		if err != nil {
			return fmt.Errorf("Telegram initialization failed")
		}
		r.Username = me.Username
		poll, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			if dashboard.Heartbeat(poll, filepath.Join(filepath.Dir(cfg.DatabasePath), "heartbeat")) != nil {
				stop()
			}
		}()
		worker := notifications.Worker{Store: db, Send: r.NotificationSender(b)}
		if err = worker.Recover(ctx); err != nil {
			return fmt.Errorf("notification recovery failed")
		}
		workerDone := make(chan error, 1)
		cleanupDone := make(chan struct{})
		go func() {
			defer close(cleanupDone)
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-poll.Done():
					return
				case <-ticker.C:
					if r.AI != nil {
						if e := r.AI.Prune(poll); e != nil {
							stop()
							return
						}
					}
				}
			}
		}()
		go func() {
			e := worker.Run(poll)
			workerDone <- e
			if e != nil {
				stop()
			}
		}()
		fmt.Println("Telegram prototype polling started")
		b.Start(poll)
		stop()
		<-cleanupDone
		<-heartbeatDone
		return <-workerDone
	case "check":
		if cfg.BotToken == "" {
			return fmt.Errorf("BOT_TOKEN is missing")
		}
		b, err := bot.New(cfg.BotToken, bot.WithSkipGetMe(), bot.WithErrorsHandler(func(error) {}))
		if err != nil {
			return fmt.Errorf("invalid Telegram configuration")
		}
		me, err := b.GetMe(ctx)
		if err != nil {
			return fmt.Errorf("Telegram credential check failed")
		}
		fmt.Printf("BOT_OK @%s\n", me.Username)
		return nil
	case "migrate", "bootstrap":
		db, err := store.Open(ctx, cfg.DatabasePath)
		if err != nil {
			return fmt.Errorf("database initialization failed: %w", err)
		}
		defer db.DB.Close()
		if command == "migrate" {
			fmt.Println("SCHEMA_OK version=2")
			return nil
		}
		ids := identity.Service{Store: db}
		token, err := ids.Bootstrap(ctx)
		if err != nil {
			return err
		}
		// This explicit local command prints an invitation bearer token, never API keys.
		fmt.Printf("Owner invitation (7 days): /start %s\n", token)
		return nil
	default:
		return fmt.Errorf("commands: check, check-ai, check-ai-flow, migrate, bootstrap, serve, panel, backup, verify-backup")
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
