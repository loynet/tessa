package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPHandlerServesHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	httpHandler(newMetrics(), &atomic.Bool{}).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 OK", response.Code)
	}
}

func TestHTTPHandlerReportsReadiness(t *testing.T) {
	var ready atomic.Bool
	handler := httpHandler(newMetrics(), &ready)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 Service Unavailable", response.Code)
	}

	ready.Store(true)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 OK", response.Code)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTelegramReadinessAndMetrics(t *testing.T) {
	var ready atomic.Bool
	metrics := newMetrics()
	metrics.ready = &ready
	handler := httpHandler(metrics, &ready)
	for _, tc := range []struct {
		ready  bool
		status int
	}{{false, 503}, {true, 200}, {false, 503}} {
		metrics.SetTelegramReady(tc.ready)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != tc.status {
			t.Fatalf("readiness status=%d want=%d", response.Code, tc.status)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		expected := "tessa_telegram_worker_ready 0"
		if tc.ready {
			expected = "tessa_telegram_worker_ready 1"
		}
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("worker gauge missing: %s", response.Body.String())
		}
	}
	metrics.ObserveTelegramRead("getMe", "server")
	metrics.ObserveTelegramRead("getUpdates", "transport")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range []string{`tessa_telegram_read_requests_total{method="getMe",result="server"} 1`, `tessa_telegram_read_requests_total{method="getUpdates",result="transport"} 1`} {
		if !strings.Contains(response.Body.String(), line) {
			t.Fatalf("missing metric %s", line)
		}
	}
}
