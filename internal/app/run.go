package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/loynet/ptchan-ai/context/thread"
	"github.com/loynet/ptchan-ai/deepseek"
	"github.com/loynet/ptchan-gateway/clients/go"

	"tessa/internal/chat"
	chatstate "tessa/internal/chat/state"
	"tessa/internal/localization"
	"tessa/internal/storage"
	"tessa/internal/telegram"
)

func Run(ctx context.Context, cfg Config, db *storage.DB, telegramClient *telegram.Client, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	metrics := newMetrics()
	var ready atomic.Bool
	metrics.ready = &ready
	server, errors, err := startHTTPServer(cfg.Runtime.HTTPAddr, metrics, &ready, logger.With("worker", "http"))
	if err != nil {
		return err
	}
	defer ready.Store(false)
	defer shutdownMetricsServer(server, logger)
	store, err := chatstate.New(db)
	if err != nil {
		return err
	}
	client, err := gateway.New(cfg.Ptchan.GatewayURL, gateway.Credentials{Name: cfg.Ptchan.IntegrationName, Secret: cfg.Ptchan.Secret})
	if err != nil {
		return fmt.Errorf("create ptchan gateway client: %w", err)
	}
	a, err := chat.New(
		cfg.Chat,
		localization.New(cfg.Locale),
		store,
		telegramClient,
		deepseek.New(cfg.DeepSeek.APIKey, cfg.DeepSeek.Model, cfg.DeepSeek.MaxTokens),
		metrics,
		logger.With("worker", "chat"),
		cfg.DeepSeek.Model,
		thread.New(cfg.ThreadContext, client, logger.With("worker", "chat", "context", "ptchan")),
	)
	if err != nil {
		return fmt.Errorf("create chat assistant: %w", err)
	}
	errc := make(chan error, 1)
	go func() { errc <- a.Run(ctx) }()
	select {
	case <-ctx.Done():
		<-errc
		return nil
	case err := <-errors:
		cancel()
		<-errc
		return fmt.Errorf("http server: %w", err)
	case err := <-errc:
		return err
	}
}

func startHTTPServer(addr string, metrics *metrics, ready *atomic.Bool, logger *slog.Logger) (*http.Server, <-chan error, error) {
	if addr == "" {
		return nil, nil, nil
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen for http: %w", err)
	}
	s := &http.Server{Handler: httpHandler(metrics, ready), ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() {
		if err := s.Serve(l); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()
	logger.Info("http listening", "address", l.Addr().String())
	return s, errs, nil
}

func httpHandler(metrics *metrics, ready *atomic.Bool) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", healthHandler)
	m.HandleFunc("/readyz", readinessHandler(ready))
	m.Handle("/metrics", metrics.handler())
	return m
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func readinessHandler(ready *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if ready == nil || !ready.Load() {
			http.Error(w, "not ready", 503)
			return
		}
		healthHandler(w, nil)
	}
}

func CheckHealth(addr string) error {
	if addr == "" {
		addr = "127.0.0.1:9090"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	r, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("health check status %s", r.Status)
	}
	return nil
}

func shutdownMetricsServer(s *http.Server, l *slog.Logger) {
	if s == nil {
		return
	}
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if err := s.Shutdown(ctx); err != nil {
		l.Warn("http server shutdown failed", "error", err)
	}
}
