package telegram

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestClientGetUpdates(t *testing.T) {
	var gotRequest *http.Request
	client := &Client{
		BaseURL: "https://api.telegram.org/bottoken",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotRequest = req
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":[{"update_id":11,"message":{"message_id":7,"text":"hello"}}]}`)),
			}, nil
		})},
	}

	updates, err := client.GetUpdates(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if len(updates) != 1 || updates[0].ID != 11 || updates[0].Message.Text != "hello" {
		t.Fatalf("updates = %+v", updates)
	}

	body, err := io.ReadAll(gotRequest.Body)
	if err != nil {
		t.Fatal(err)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if gotRequest.URL.Path != "/bottoken/getUpdates" || form.Get("offset") != "10" || form.Get("timeout") != "30" || form.Get("allowed_updates") != `["message"]` {
		t.Fatalf("request path=%q form=%v", gotRequest.URL.Path, form)
	}
}

func TestIncomingMessageAddressesMentionWithUTF16Offset(t *testing.T) {
	message := IncomingMessage{
		Text: "😀 hey @MartieBot",
		Entities: []MessageEntity{
			{Type: "mention", Offset: 7, Length: 10},
		},
	}

	if !message.Addresses(User{ID: 42, Username: "martiebot"}) {
		t.Fatal("Addresses() = false, want true")
	}
}

func TestIncomingMessageAddressesReply(t *testing.T) {
	message := IncomingMessage{
		ReplyToMessage: &IncomingMessage{
			From: &User{ID: 42, IsBot: true},
		},
	}

	if !message.Addresses(User{ID: 42, Username: "martiebot"}) {
		t.Fatal("Addresses() = false, want true")
	}
}

func TestIncomingMessageDoesNotAddressOtherMention(t *testing.T) {
	message := IncomingMessage{
		Text: "@somebody",
		Entities: []MessageEntity{
			{Type: "mention", Offset: 0, Length: 10},
		},
	}

	if message.Addresses(User{ID: 42, Username: "martiebot"}) {
		t.Fatal("Addresses() = true, want false")
	}
}

func TestIncomingMessageTextWithoutMention(t *testing.T) {
	message := IncomingMessage{
		Text: "😀 hey @MartieBot, help please",
		Entities: []MessageEntity{
			{Type: "mention", Offset: 7, Length: 10},
		},
	}

	if got := message.TextWithoutMention(User{Username: "martiebot"}); got != "😀 hey , help please" {
		t.Fatalf("TextWithoutMention() = %q", got)
	}
}

func TestIncomingMessageTextWithoutMentionLeavesOtherMentions(t *testing.T) {
	message := IncomingMessage{
		Text: "@someone tell @martiebot hello",
		Entities: []MessageEntity{
			{Type: "mention", Offset: 0, Length: 8},
			{Type: "mention", Offset: 14, Length: 10},
		},
	}

	if got := message.TextWithoutMention(User{Username: "martiebot"}); got != "@someone tell  hello" {
		t.Fatalf("TextWithoutMention() = %q", got)
	}
}

func TestIncomingMessageMentions(t *testing.T) {
	message := IncomingMessage{
		Text: "ask @alice and @bob",
		Entities: []MessageEntity{
			{Type: "mention", Offset: 4, Length: 6},
			{Type: "mention", Offset: 15, Length: 4},
		},
	}

	mentions := message.Mentions()
	if len(mentions) != 2 || mentions[0] != "alice" || mentions[1] != "bob" {
		t.Fatalf("Mentions() = %q", mentions)
	}
}

func TestEntityTextRejectsInvalidBounds(t *testing.T) {
	if _, ok := entityText("hello", MessageEntity{Offset: 4, Length: 2}); ok {
		t.Fatal("entityText() accepted an out-of-bounds entity")
	}
}
