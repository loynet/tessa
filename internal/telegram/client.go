package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

type APIError struct {
	StatusCode  int
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	RetryAfter  int    `json:"-"`
	Body        []byte `json:"-"`
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("telegram api error: HTTP status %d", e.StatusCode)
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram api error: %s (retry after %ds)", e.Description, e.RetryAfter)
	}
	return "telegram api error: " + e.Description
}

func New(botToken string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &Client{
		BaseURL: fmt.Sprintf("https://api.telegram.org/bot%s", botToken),
		Logger:  logger.With("component", "telegram"),
		HTTPClient: &http.Client{
			Timeout: 40 * time.Second,
		},
	}
}

type SendRequest struct {
	ChatID           int64
	Message          OutgoingMessage
	ReplyToMessageID int64
	MessageThreadID  int64
}

func (c *Client) SendTyping(ctx context.Context, chatID, messageThreadID int64) error {
	form := url.Values{}
	form.Set("chat_id", fmt.Sprintf("%d", chatID))
	form.Set("action", "typing")
	if messageThreadID != 0 {
		form.Set("message_thread_id", fmt.Sprintf("%d", messageThreadID))
	}
	return c.do(ctx, "sendChatAction", form, nil)
}

// Send uses the Bot API sendMessage method:
// https://core.telegram.org/bots/api#sendmessage
//
// We rely on Telegram's default link preview behavior described here:
// https://core.telegram.org/bots/api#linkpreviewoptions
func (c *Client) Send(ctx context.Context, request SendRequest) error {
	form := url.Values{}
	form.Set("chat_id", fmt.Sprintf("%d", request.ChatID))
	form.Set("text", request.Message.text)
	if request.Message.parseMode != "" {
		form.Set("parse_mode", request.Message.parseMode)
	}
	if request.MessageThreadID != 0 {
		form.Set("message_thread_id", fmt.Sprintf("%d", request.MessageThreadID))
	}
	if request.ReplyToMessageID != 0 {
		replyParameters, err := json.Marshal(struct {
			MessageID int64 `json:"message_id"`
		}{MessageID: request.ReplyToMessageID})
		if err != nil {
			return fmt.Errorf("encode reply parameters: %w", err)
		}
		form.Set("reply_parameters", string(replyParameters))
	}

	err := c.do(ctx, "sendMessage", form, nil)
	var apiErr *APIError
	if request.Message.parseMode != "" && errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Description), "can't parse entities") {
		if c.Logger != nil {
			c.Logger.Warn("telegram markdown rejected; retrying as plain text", "chat_id", request.ChatID, "error", err)
		}
		form.Del("parse_mode")
		err = c.do(ctx, "sendMessage", form, nil)
	}
	return err
}

func (c *Client) do(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return &TransportError{Err: err}
	}
	if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Hostname() == "" {
		return &TransportError{Err: errors.New("invalid telegram endpoint")}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return &TransportError{Err: err, Retryable: temporaryTransportError(err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if resp.StatusCode != http.StatusOK {
			return &APIError{StatusCode: resp.StatusCode}
		}
		return &TransportError{Err: err, Retryable: temporaryTransportError(err)}
	}
	if len(body) > maxResponseBytes {
		if resp.StatusCode != http.StatusOK {
			return &APIError{StatusCode: resp.StatusCode}
		}
		return fmt.Errorf("telegram api response exceeds %d bytes", maxResponseBytes)
	}

	var result struct {
		ErrorCode   int             `json:"error_code"`
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		if resp.StatusCode != http.StatusOK {
			return &APIError{StatusCode: resp.StatusCode}
		}
		return fmt.Errorf("decode telegram response: %w", err)
	}

	if !result.OK {
		return &APIError{
			StatusCode:  resp.StatusCode,
			ErrorCode:   result.ErrorCode,
			Description: result.Description,
			RetryAfter:  result.Parameters.RetryAfter,
			Body:        append([]byte(nil), body...),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode}
	}
	if out != nil && len(result.Result) > 0 {
		if err := json.Unmarshal(result.Result, out); err != nil {
			return fmt.Errorf("decode telegram result: %w", err)
		}
	}

	return nil
}
