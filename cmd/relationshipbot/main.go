package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/ai"
	"github.com/NikitaKashko/RelationshipChatBot/internal/config"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/telegram"
	"github.com/go-telegram/bot"
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
		fmt.Println("Telegram registration polling started; full release still in development")
		b.Start(poll)
		stop()
		<-cleanupDone
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
			fmt.Println("SCHEMA_OK version=1")
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
		return fmt.Errorf("commands: check, check-ai, check-ai-flow, migrate, bootstrap, serve")
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
