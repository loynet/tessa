package telegram

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
		retry  bool
		delay  time.Duration
	}{
		{"proxy 502", 502, "secret proxy diagnostics", "server", true, 0},
		{"proxy 503", 503, `{"ok":false,"error_code":503,"description":"secret"}`, "server", true, 0},
		{"rate limit", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":75},"description":"secret"}`, "rate_limited", true, 75 * time.Second},
		{"API rate limit", 200, `{"ok":false,"error_code":429,"parameters":{"retry_after":75}}`, "rate_limited", true, 75 * time.Second},
		{"credentials", 401, "secret", "credentials", false, 0},
		{"conflict", 409, `{"ok":false,"error_code":409}`, "conflict", false, 0},
		{"bad request", 400, `{"ok":false,"error_code":400}`, "permanent", false, 0},
		{"invalid JSON", 200, "secret", "protocol", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{BaseURL: "https://api.telegram.org/botsecret", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			_, err := client.GetMe(context.Background())
			reason, delay, retry := ClassifyError(err)
			if err == nil || reason != tc.reason || delay != tc.delay || retry != tc.retry {
				t.Fatalf("err=%v reason=%s delay=%s retry=%t", err, reason, delay, retry)
			}
		})
	}
}

func TestTransportErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"timeout", context.DeadlineExceeded, true},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}, true},
		{"truncated", io.ErrUnexpectedEOF, true},
		{"temporary DNS", &net.DNSError{IsTemporary: true}, true},
		{"invalid DNS", &net.DNSError{IsNotFound: true}, false},
		{"HTTP2 GOAWAY", errors.New("http2: server sent GOAWAY and closed the connection; debug=secret"), true},
		{"HTTP2 stream reset", errors.New("stream error: stream ID 1; INTERNAL_ERROR; received from peer"), true},
		{"unknown authority", x509.UnknownAuthorityError{}, false},
		{"hostname mismatch", x509.HostnameError{Host: "secret"}, false},
		{"expired certificate", x509.CertificateInvalidError{Reason: x509.Expired}, false},
		{"certificate verification", &tls.CertificateVerificationError{Err: errors.New("secret")}, false},
		{"invalid TLS endpoint", tls.RecordHeaderError{Msg: "secret"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{BaseURL: "https://api.telegram.org/botsecret", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err })}}
			_, err := client.GetMe(context.Background())
			_, _, retry := ClassifyError(err)
			if retry != tc.retry || strings.Contains(err.Error(), "secret") {
				t.Fatalf("err=%v retry=%t", err, retry)
			}
		})
	}
	for _, endpoint := range []string{"://secret", "ftp://secret", "https:///botsecret"} {
		client := &Client{BaseURL: endpoint}
		_, err := client.GetMe(context.Background())
		reason, _, retry := ClassifyError(err)
		if reason != "configuration" || retry || strings.Contains(err.Error(), "secret") {
			t.Fatalf("configuration err=%v retry=%t", err, retry)
		}
	}
}

func TestSendDoesNotRetryTemporaryFailure(t *testing.T) {
	calls := 0
	client := &Client{BaseURL: "https://api.telegram.org/botsecret", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})}}
	err := client.Send(context.Background(), SendRequest{Message: MarkdownMessage("reply")})
	if err == nil || calls != 1 {
		t.Fatalf("send calls=%d err=%v", calls, err)
	}
}

func TestHTTP2StreamFailureIsRetryable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("protocol = %s, want HTTP/2", r.Proto)
		}
		if calls.Add(1) == 1 {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":99,"username":"tessa_bot"}}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
	client.HTTPClient.Timeout = 3 * time.Second
	_, err := client.GetMe(context.Background())
	reason, _, retry := ClassifyError(err)
	if err == nil || reason != "transport" || !retry {
		t.Fatalf("HTTP/2 reset: error=%v reason=%s retry=%t", err, reason, retry)
	}
	user, err := client.GetMe(context.Background())
	if err != nil || user.ID != 99 || calls.Load() != 2 {
		t.Fatalf("recovery: error=%v user=%+v calls=%d", err, user, calls.Load())
	}
}
