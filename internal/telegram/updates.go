package telegram

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf16"
)

const entityMention = "mention"

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type IncomingMessage struct {
	ID                 int64            `json:"message_id"`
	MessageThreadID    int64            `json:"message_thread_id"`
	From               *User            `json:"from"`
	Chat               Chat             `json:"chat"`
	Text               string           `json:"text"`
	Entities           []MessageEntity  `json:"entities"`
	IsAutomaticForward bool             `json:"is_automatic_forward"`
	ReplyToMessage     *IncomingMessage `json:"reply_to_message"`
}

type Update struct {
	ID      int64            `json:"update_id"`
	Message *IncomingMessage `json:"message"`
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	if err := c.do(ctx, "getMe", nil, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	form := url.Values{}
	form.Set("offset", fmt.Sprintf("%d", offset))
	form.Set("timeout", "30")
	form.Set("allowed_updates", `["message"]`)
	var updates []Update
	if err := c.do(ctx, "getUpdates", form, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (m IncomingMessage) Addresses(bot User) bool {
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == bot.ID {
		return true
	}

	for _, entity := range m.Entities {
		if entity.Type != entityMention {
			continue
		}
		mention, ok := entityText(m.Text, entity)
		if ok && strings.EqualFold(mention, "@"+bot.Username) {
			return true
		}
	}

	return false
}

func (m IncomingMessage) Mentions() []string {
	var mentions []string
	for _, entity := range m.Entities {
		if entity.Type != entityMention {
			continue
		}
		mention, ok := entityText(m.Text, entity)
		if ok {
			mentions = append(mentions, strings.TrimPrefix(mention, "@"))
		}
	}
	return mentions
}

func (m IncomingMessage) TextWithoutMention(bot User) string {
	encoded := utf16.Encode([]rune(m.Text))
	type span struct{ start, end int }
	var spans []span
	for _, entity := range m.Entities {
		if entity.Type != entityMention {
			continue
		}
		mention, ok := entityText(m.Text, entity)
		if !ok || !strings.EqualFold(mention, "@"+bot.Username) {
			continue
		}
		spans = append(spans, span{start: entity.Offset, end: entity.Offset + entity.Length})
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	for _, span := range spans {
		encoded = append(encoded[:span.start], encoded[span.end:]...)
	}
	return strings.TrimSpace(string(utf16.Decode(encoded)))
}

func entityText(text string, entity MessageEntity) (string, bool) {
	encoded := utf16.Encode([]rune(text))
	end := entity.Offset + entity.Length
	if entity.Offset < 0 || entity.Length < 0 || end > len(encoded) {
		return "", false
	}
	return string(utf16.Decode(encoded[entity.Offset:end])), true
}
