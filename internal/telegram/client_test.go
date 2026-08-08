package telegram

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestClientSend(t *testing.T) {
	var gotRequest *http.Request

	client := &Client{
		BaseURL: "https://api.telegram.org/bottoken",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				gotRequest = req

				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)),
				}, nil
			}),
		},
	}

	err := client.Send(context.Background(), SendRequest{
		ChatID:           12345,
		Message:          MarkdownMessage("*Hello*"),
		ReplyToMessageID: 7,
		MessageThreadID:  9,
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotRequest == nil {
		t.Fatal("request was not sent")
	}

	if gotRequest.URL.Path != "/bottoken/sendMessage" {
		t.Fatalf("path = %q, want %q", gotRequest.URL.Path, "/bottoken/sendMessage")
	}
	if got := gotRequest.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/x-www-form-urlencoded")
	}

	body, err := io.ReadAll(gotRequest.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("parse form body: %v", err)
	}

	if got := form.Get("chat_id"); got != "12345" {
		t.Fatalf("chat_id = %q, want %q", got, "12345")
	}
	if got := form.Get("text"); got != "*Hello*" {
		t.Fatalf("text = %q, want %q", got, "*Hello*")
	}
	if got := form.Get("parse_mode"); got != "Markdown" {
		t.Fatalf("parse_mode = %q, want %q", got, "Markdown")
	}
	if got := form.Get("message_thread_id"); got != "9" {
		t.Fatalf("message_thread_id = %q, want %q", got, "9")
	}
	if got := form.Get("reply_parameters"); got != `{"message_id":7}` {
		t.Fatalf("reply_parameters = %q, want %q", got, `{"message_id":7}`)
	}
	if got := form.Get("disable_web_page_preview"); got != "" {
		t.Fatalf("disable_web_page_preview = %q, want empty", got)
	}
}

func TestClientSendFallsBackToPlainTextForInvalidMarkdown(t *testing.T) {
	var forms []url.Values
	var logs bytes.Buffer
	client := &Client{
		BaseURL: "https://api.telegram.org/bottoken",
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			forms = append(forms, form)
			response := `{"ok":false,"description":"Bad Request: can't parse entities"}`
			if len(forms) == 2 {
				response = `{"ok":true,"result":{"message_id":1}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
		})},
	}

	err := client.Send(context.Background(), SendRequest{ChatID: 12345, Message: MarkdownMessage("bad markdown")})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(forms) != 2 {
		t.Fatalf("requests = %d, want 2", len(forms))
	}
	if got := forms[1].Get("text"); got != "bad markdown" {
		t.Fatalf("fallback text = %q, want original message", got)
	}
	if got := forms[1].Get("parse_mode"); got != "" {
		t.Fatalf("fallback parse_mode = %q, want empty", got)
	}
	if !strings.Contains(logs.String(), "telegram markdown rejected") || !strings.Contains(logs.String(), "can't parse entities") {
		t.Fatalf("fallback log = %q", logs.String())
	}
}

func TestClientSendAPIFailure(t *testing.T) {
	client := &Client{
		BaseURL: "https://api.telegram.org/bottoken",
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"ok":false,"description":"chat not found"}`)),
				}, nil
			}),
		},
	}

	err := client.Send(context.Background(), SendRequest{ChatID: 12345, Message: TextMessage("hello")})
	if err == nil {
		t.Fatal("Send() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("Send() error = %q, want to contain %q", err, "chat not found")
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	client := &Client{
		BaseURL: "https://api.telegram.org/bottoken",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
			}, nil
		})},
	}

	err := client.Send(context.Background(), SendRequest{ChatID: 12345, Message: TextMessage("hello")})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("Send() error = %v, want oversized response error", err)
	}
}

func TestClientSendTyping(t *testing.T) {
	var gotRequest *http.Request
	client := &Client{
		BaseURL: "https://api.telegram.org/bottoken",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotRequest = req
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)),
			}, nil
		})},
	}

	if err := client.SendTyping(context.Background(), 12345, 9); err != nil {
		t.Fatalf("SendTyping() error = %v", err)
	}
	body, err := io.ReadAll(gotRequest.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("parse form body: %v", err)
	}
	if gotRequest.URL.Path != "/bottoken/sendChatAction" || form.Get("chat_id") != "12345" || form.Get("message_thread_id") != "9" || form.Get("action") != "typing" {
		t.Fatalf("SendTyping() request path=%q form=%v", gotRequest.URL.Path, form)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
