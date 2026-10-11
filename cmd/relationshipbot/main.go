package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/config"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
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
		fmt.Println("Telegram registration polling started; full release still in development")
		b.Start(poll)
		return nil
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
		return fmt.Errorf("commands: check, migrate, bootstrap, serve")
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
