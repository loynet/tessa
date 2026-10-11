package chat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"tessa/internal/telegram"
)

type telegramTransport func(*http.Request) (*http.Response, error)

func (f telegramTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func telegramResponse(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

type workerMetrics struct {
	*fakeMetrics
	ready   bool
	results []string
	states  []bool
}

func (m *workerMetrics) ObserveTelegramRead(method, result string) {
	m.results = append(m.results, method+":"+result)
}
func (m *workerMetrics) SetTelegramReady(ready bool) {
	m.ready = ready
	m.states = append(m.states, ready)
}

type recoveryCursor struct {
	position int64
	writes   []int64
}

func (s *recoveryCursor) GetCursor(context.Context, string) (int64, bool, error) {
	return s.position, true, nil
}
func (s *recoveryCursor) SetCursor(_ context.Context, _ string, offset int64) error {
	s.position = offset
	s.writes = append(s.writes, offset)
	return nil
}

func TestTelegramReadRecoveryPreservesCursorAndHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sender := &fakeSender{}
		completer := &recordingCompleter{answers: []string{"first answer", "second answer"}}
		c := testChatterHandler(completer, sender)
		store := &recoveryCursor{position: 41}
		m := &workerMetrics{fakeMetrics: newFakeMetrics()}
		c.store, c.metrics = store, m
		var logs bytes.Buffer
		c.logger = slog.New(slog.NewTextHandler(&logs, nil))
		var identityCalls, polls int
		var offsets []string
		var identityTimes, pollTimes []time.Time
		c.client = &telegram.Client{BaseURL: "https://api.telegram.org/botSECRET", HTTPClient: &http.Client{Transport: telegramTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/getMe") {
				identityCalls++
				identityTimes = append(identityTimes, time.Now())
				switch identityCalls {
				case 1:
					return telegramResponse(502, "SECRET proxy error")
				case 2:
					return telegramResponse(503, `{"ok":false,"error_code":503,"description":"SECRET"}`)
				default:
					return telegramResponse(200, `{"ok":true,"result":{"id":99,"username":"martie_bot"}}`)
				}
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			polls++
			offsets = append(offsets, r.Form.Get("offset"))
			pollTimes = append(pollTimes, time.Now())
			if polls > 1 && polls < 5 && len(c.history) != 1 {
				t.Fatal("history lost during outage")
			}
			switch polls {
			case 1, 5:
				id := 41
				if polls == 5 {
					id = 42
					if m.ready {
						t.Fatal("worker ready before recovery")
					}
				}
				return telegramResponse(200, fmtUpdate(id))
			case 2:
				return telegramResponse(502, "SECRET gateway failure")
			case 3:
				if m.ready {
					t.Fatal("worker ready during outage")
				}
				return telegramResponse(503, `{"ok":false,"error_code":503,"description":"SECRET"}`)
			case 4:
				return nil, context.DeadlineExceeded
			default:
				if !m.ready {
					t.Fatal("worker not ready after successful poll")
				}
				cancel()
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		})}}
		if err := c.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if identityCalls != 3 || polls != 6 {
			t.Fatalf("identity calls=%d polls=%d", identityCalls, polls)
		}
		if !reflect.DeepEqual(offsets, []string{"41", "42", "42", "42", "42", "43"}) || !reflect.DeepEqual(store.writes, []int64{42, 43}) {
			t.Fatalf("offsets=%v writes=%v", offsets, store.writes)
		}
		if len(sender.requests) != 2 || len(completer.calls) != 2 || len(completer.calls[1]) != 3 || completer.calls[1][1].Content != "first answer" {
			t.Fatalf("replies or history lost: requests=%d calls=%v", len(sender.requests), completer.calls)
		}
		for _, times := range [][]time.Time{identityTimes, pollTimes[1:5]} {
			for i := 1; i < len(times); i++ {
				floor := time.Second * time.Duration(1<<(i-1)) / 2
				if times[i].Sub(times[i-1]) < floor {
					t.Fatalf("retry too soon: %v", times)
				}
			}
		}
		if m.ready {
			t.Fatal("ready after shutdown")
		}
		if !strings.Contains(strings.Join(m.results, ","), "getUpdates:transport") {
			t.Fatalf("missing transport metric: %v", m.results)
		}
		if strings.Contains(logs.String(), "SECRET") || !strings.Contains(logs.String(), "telegram read recovered") {
			t.Fatalf("logs=%s", logs.String())
		}
	})
}

func fmtUpdate(id int) string {
	// Two addressed messages in the same conversation; IDs must follow the cursor.
	if id == 41 {
		return `{"ok":true,"result":[{"update_id":41,"message":{"message_id":1,"chat":{"id":100},"from":{"id":10},"text":"@martie_bot first","entities":[{"type":"mention","offset":0,"length":11}]}}]}`
	}
	return `{"ok":true,"result":[{"update_id":42,"message":{"message_id":2,"chat":{"id":100},"from":{"id":10},"text":"@martie_bot second","entities":[{"type":"mention","offset":0,"length":11}]}}]}`
}

func TestTelegramRetryAfterAndCancellation(t *testing.T) {
	for _, method := range []string{"getMe", "getUpdates"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := testChatterHandler(&fakeCompleter{}, &fakeSender{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				started := time.Now()
				err := c.readTelegram(ctx, method, func() error {
					calls++
					if calls == 1 {
						return &telegram.APIError{StatusCode: 429, RetryAfter: 75}
					}
					if time.Since(started) < 75*time.Second {
						t.Fatal("retried before retry_after")
					}
					return nil
				})
				if err != nil || calls != 2 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
				done := make(chan error, 1)
				go func() {
					done <- c.readTelegram(ctx, method, func() error { return &telegram.APIError{StatusCode: 429, RetryAfter: 3600} })
				}()
				synctest.Wait()
				canceledAt := time.Now()
				cancel()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) || time.Since(canceledAt) != 0 {
					t.Fatalf("slow cancellation: %v", err)
				}
			})
		})
	}
}

func TestTelegramPersistentOutageIsBoundedAndObservable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testChatterHandler(&fakeCompleter{}, &fakeSender{})
		m := &workerMetrics{fakeMetrics: newFakeMetrics()}
		c.metrics = m
		var logs bytes.Buffer
		c.logger = slog.New(slog.NewTextHandler(&logs, nil))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var times []time.Time
		done := make(chan error, 1)
		go func() {
			done <- c.readTelegram(ctx, "getMe", func() error {
				times = append(times, time.Now())
				return &telegram.APIError{StatusCode: 503, Description: "SECRET"}
			})
		}()
		time.Sleep(5 * time.Minute)
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if m.ready || len(m.results) != len(times) || len(times) < 10 || len(times) > 30 {
			t.Fatalf("outage metrics=%v calls=%d ready=%t", m.results, len(times), m.ready)
		}
		for i := 1; i < len(times); i++ {
			maxDelay := min(time.Second*time.Duration(1<<min(i-1, 5)), telegramMaxBackoff)
			gap := times[i].Sub(times[i-1])
			if gap < maxDelay/2 || gap > maxDelay {
				t.Fatalf("unbounded backoff %v at %d", gap, i)
			}
		}
		if strings.Count(logs.String(), "unavailable") > 5 || strings.Contains(logs.String(), "SECRET") {
			t.Fatalf("unbounded or unsafe logs: %s", logs.String())
		}
	})
}

func TestTelegramPermanentReadFailures(t *testing.T) {
	for _, tc := range []struct {
		code    int
		message string
	}{{401, "credentials rejected"}, {404, "credentials rejected"}, {409, "polling conflict"}, {400, "permanent error"}} {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			c := testChatterHandler(&fakeCompleter{}, &fakeSender{})
			calls := 0
			c.store = &recoveryCursor{}
			c.client = &telegram.Client{BaseURL: "https://api.telegram.org/botSECRET", HTTPClient: &http.Client{Transport: telegramTransport(func(r *http.Request) (*http.Response, error) {
				if tc.code == 409 && strings.HasSuffix(r.URL.Path, "/getMe") {
					return telegramResponse(200, `{"ok":true,"result":{"id":99,"username":"martie_bot"}}`)
				}
				calls++
				// Bot API error_code remains authoritative even when HTTP status is 200.
				body := `{"ok":false,"error_code":` + strconv.Itoa(tc.code) + `,"description":"SECRET"}`
				return telegramResponse(200, body)
			})}}
			err := c.Run(context.Background())
			if calls != 1 || err == nil || !strings.Contains(err.Error(), tc.message) || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestTelegramCancellationDuringRead(t *testing.T) {
	for _, method := range []string{"getMe", "getUpdates"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := testChatterHandler(&fakeCompleter{}, &fakeSender{})
				c.store = &recoveryCursor{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c.client = &telegram.Client{BaseURL: "https://api.telegram.org/botSECRET", HTTPClient: &http.Client{Transport: telegramTransport(func(r *http.Request) (*http.Response, error) {
					if method == "getUpdates" && strings.HasSuffix(r.URL.Path, "/getMe") {
						return telegramResponse(200, `{"ok":true,"result":{"id":99,"username":"martie_bot"}}`)
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}}
				done := make(chan error, 1)
				go func() { done <- c.Run(ctx) }()
				synctest.Wait()
				canceledAt := time.Now()
				cancel()
				synctest.Wait()
				if err := <-done; err != nil || time.Since(canceledAt) != 0 {
					t.Fatalf("slow or unsuccessful shutdown: %v", err)
				}
			})
		})
	}
}

func TestTelegramHTTP2FailuresRecoverWithBackoff(t *testing.T) {
	for _, method := range []string{"getMe", "getUpdates"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := testChatterHandler(&fakeCompleter{}, &fakeSender{})
				var logs bytes.Buffer
				c.logger = slog.New(slog.NewTextHandler(&logs, nil))
				var calls []time.Time
				c.client = &telegram.Client{BaseURL: "https://api.telegram.org/botSECRET", HTTPClient: &http.Client{Transport: telegramTransport(func(*http.Request) (*http.Response, error) {
					calls = append(calls, time.Now())
					switch len(calls) {
					case 1:
						return nil, errors.New("http2: server sent GOAWAY and closed the connection; debug=SECRET")
					case 2:
						return nil, errors.New("stream error: stream ID 1; INTERNAL_ERROR; received from peer")
					default:
						if method == "getMe" {
							return telegramResponse(200, `{"ok":true,"result":{"id":99,"username":"tessa_bot"}}`)
						}
						return telegramResponse(200, `{"ok":true,"result":[]}`)
					}
				})}}
				err := c.readTelegram(context.Background(), method, func() error {
					if method == "getMe" {
						_, err := c.client.GetMe(context.Background())
						return err
					}
					_, err := c.client.GetUpdates(context.Background(), 42)
					return err
				})
				if err != nil || len(calls) != 3 {
					t.Fatalf("calls=%v error=%v", calls, err)
				}
				if calls[1].Sub(calls[0]) < 500*time.Millisecond || calls[2].Sub(calls[1]) < time.Second {
					t.Fatalf("no backoff: %v", calls)
				}
				if strings.Contains(logs.String(), "SECRET") || !strings.Contains(logs.String(), "recovered") {
					t.Fatalf("logs=%s", logs.String())
				}
			})
		})
	}
}
