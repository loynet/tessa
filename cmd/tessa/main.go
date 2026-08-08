package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"tessa/internal/app"
	"tessa/internal/storage"
	"tessa/internal/telegram"
)

func main() {
	cmd, err := parseCommand(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if cmd == "check-health" {
		if err := app.CheckHealth(os.Getenv("HEALTHCHECK_ADDR")); err != nil {
			log.Fatal(err)
		}
		return
	}
	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if cmd == "check-config" {
		return
	}
	db, err := storage.Open(cfg.SQLitePath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, db, telegram.New(cfg.Telegram.BotToken, newLogger(cfg.Runtime.Logging)), newLogger(cfg.Runtime.Logging)); err != nil {
		log.Fatal(err)
	}
}
func newLogger(c app.LoggingConfig) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: c.Level}))
}
func parseCommand(a []string) (string, error) {
	if len(a) != 1 {
		return "", fmt.Errorf("usage: tessa [run|check-config|check-health]")
	}
	switch a[0] {
	case "run", "check-config", "check-health":
		return a[0], nil
	default:
		return "", fmt.Errorf("usage: tessa [run|check-config|check-health]")
	}
}
