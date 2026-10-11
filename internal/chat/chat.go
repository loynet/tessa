package chat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/loynet/ptchan-ai/context/thread"
	"github.com/loynet/ptchan-ai/deepseek"
	"golang.org/x/time/rate"

	"tessa/internal/localization"
	"tessa/internal/telegram"
)

const (
	// These bounds protect Telegram and completion requests. They are protocol
	// safeguards rather than environment policy, so they remain in code.
	typingInterval       = 4 * time.Second
	rejectionReplyWindow = 10 * time.Second
	replyContextRunes    = 1000
	rateLimitWindow      = time.Hour

	ResultSuccess = "success"
	ResultError   = "error"
)

type Config struct {
	Name               string
	DiscussionChatID   int64
	AllowAllUsers      bool
	AllowedUserIDs     []int64
	UserPerHour        int
	UserRequestBurst   int
	GlobalPerHour      int
	GlobalRequestBurst int
	SystemPrompt       string
	MaxInputRunes      int
	ConversationTTL    time.Duration
	HistoryExchanges   int
}

type Store interface {
	GetCursor(context.Context, string) (int64, bool, error)
	SetCursor(context.Context, string, int64) error
}

// Assistant owns the conversational application flow between Telegram and the
// completion engine. It admits requests, constructs bounded context, replaces
// Telegram identities with temporary aliases, and delivers the final reply.
// Conversation history is process-local and intentionally not persisted.
type Assistant struct {
	cfg           Config
	text          localization.Localizer
	store         Store
	client        *telegram.Client
	sender        Sender
	completer     Completer
	modelName     string
	metrics       Metrics
	logger        *slog.Logger
	allowed       map[int64]struct{}
	mu            sync.Mutex
	global        *rate.Limiter
	users         map[int64]userRateLimiter
	replies       *rate.Limiter
	history       map[conversationKey]*conversation
	threadContext *thread.Context
	aliasSeed     string
}

type userRateLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type admissionResult string

const (
	admissionAccepted         admissionResult = "accepted"
	admissionUnsupported      admissionResult = "unsupported"
	admissionWrongChat        admissionResult = "wrong_chat"
	admissionAutomaticForward admissionResult = "automatic_forward"
	admissionBot              admissionResult = "bot"
	admissionUnaddressed      admissionResult = "unaddressed"
	admissionEmpty            admissionResult = "empty"
	admissionTooLong          admissionResult = "too_long"
	admissionUnauthorized     admissionResult = "unauthorized"
	admissionRateLimited      admissionResult = "rate_limited"
	admissionDuplicate        admissionResult = "duplicate"
)

type Completer interface {
	Complete(context.Context, string, []deepseek.Message) (deepseek.Completion, error)
}

type Sender interface {
	Send(context.Context, telegram.SendRequest) error
	SendTyping(context.Context, int64, int64) error
}

type Metrics interface {
	ObserveTelegramPoll(duration time.Duration, err error)
	ObserveTelegramRead(method, result string)
	SetTelegramReady(ready bool)
	ObserveAdmission(result string)
	ObserveReply(result string)
	ObserveContext(contextType string)
	SetActiveConversations(count int)
	ObserveModelCompletion(provider, model string, duration time.Duration, completion deepseek.Completion, err error)
}

// Request is the admitted subset of a Telegram message. Its text and
// identity fields remain untrusted until prompt construction and aliasing.
type Request struct {
	MessageID       int64
	MessageThreadID int64
	UserID          int64
	Username        string
	FirstName       string
	ChatTitle       string
	Text            string
	ReplyText       string
	ReplyFromBot    bool
	ReplyUserID     int64
	ReplyUsername   string
	ReplyFirstName  string
	Mentions        []string
	ReplyMentions   []string
}

type preparedRequest struct {
	key          conversationKey
	conversation *conversation
	messages     []deepseek.Message
	userAlias    string
	userMessage  string
}

func New(cfg Config, text localization.Localizer, store Store, client *telegram.Client, completer Completer, metrics Metrics, logger *slog.Logger, modelName string, threadContext *thread.Context) (*Assistant, error) {
	aliasSeed, err := randomAliasSeed()
	if err != nil {
		return nil, err
	}
	allowed := make(map[int64]struct{}, len(cfg.AllowedUserIDs))
	for _, userID := range cfg.AllowedUserIDs {
		allowed[userID] = struct{}{}
	}

	return &Assistant{
		cfg:           cfg,
		text:          text,
		store:         store,
		client:        client,
		sender:        client,
		completer:     completer,
		modelName:     modelName,
		metrics:       metrics,
		logger:        logger,
		allowed:       allowed,
		global:        hourlyRateLimiter(cfg.GlobalPerHour, cfg.GlobalRequestBurst),
		users:         make(map[int64]userRateLimiter),
		replies:       rate.NewLimiter(rate.Every(rejectionReplyWindow), 1),
		history:       make(map[conversationKey]*conversation),
		threadContext: threadContext,
		aliasSeed:     aliasSeed,
	}, nil
}

func (c *Assistant) Run(ctx context.Context) error {
	c.metrics.SetTelegramReady(false)
	defer c.metrics.SetTelegramReady(false)
	var bot telegram.User
	err := c.readTelegram(ctx, "getMe", func() error {
		var err error
		bot, err = c.client.GetMe(ctx)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if bot.Username == "" {
		return fmt.Errorf("telegram bot username is empty")
	}

	cursor := telegramUpdateCursor(bot.ID)
	offset, _, err := c.store.GetCursor(ctx, cursor)
	if err != nil {
		return fmt.Errorf("load update cursor: %w", err)
	}

	c.logger.Info("chat active", "username", "@"+bot.Username)
	// Updates stay sequential so a stored cursor always means every preceding
	// message has finished processing.
	for {
		var updates []telegram.Update
		err := c.readTelegram(ctx, "getUpdates", func() error {
			startedAt := time.Now()
			var err error
			updates, err = c.client.GetUpdates(ctx, offset)
			if ctx.Err() == nil {
				c.metrics.ObserveTelegramPoll(time.Since(startedAt), err)
			}
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		c.metrics.SetTelegramReady(true)
		c.expireConversations(time.Now())

		for _, update := range updates {
			if err := c.processUpdate(ctx, cursor, update, bot); err != nil {
				return err
			}
			offset = update.ID + 1
		}
	}
}

func (c *Assistant) processUpdate(ctx context.Context, cursor string, update telegram.Update, bot telegram.User) error {
	request, result := c.admit(update.Message, bot)
	if result == admissionWrongChat {
		c.logger.Debug("chat message ignored", "reason", result, "chat", update.Message.Chat.Title, "chat_id", update.Message.Chat.ID, "configured_chat_id", c.cfg.DiscussionChatID)
	}
	if request != nil {
		c.metrics.ObserveAdmission(string(admissionAccepted))
		if !c.handle(ctx, *request) {
			return ctx.Err()
		}
	} else {
		c.metrics.ObserveAdmission(string(result))
		c.replyToRejection(ctx, update.Message, result)
	}

	if err := c.store.SetCursor(ctx, cursor, update.ID+1); err != nil {
		return fmt.Errorf("store update cursor: %w", err)
	}
	return nil
}

func telegramUpdateCursor(botID int64) string {
	return fmt.Sprintf("telegram:%d:updates", botID)
}

func (c *Assistant) admit(message *telegram.IncomingMessage, bot telegram.User) (*Request, admissionResult) {
	if message == nil {
		return nil, admissionUnsupported
	}
	if message.Chat.ID != c.cfg.DiscussionChatID {
		return nil, admissionWrongChat
	}
	if message.IsAutomaticForward {
		return nil, admissionAutomaticForward
	}
	if message.From == nil || message.From.IsBot {
		return nil, admissionBot
	}
	if !message.Addresses(bot) {
		return nil, admissionUnaddressed
	}
	if strings.TrimSpace(message.Text) == "" {
		return nil, admissionEmpty
	}
	if utf8.RuneCountInString(message.Text) > c.cfg.MaxInputRunes {
		return nil, admissionTooLong
	}
	text := message.TextWithoutMention(bot)
	if text == "" {
		return nil, admissionEmpty
	}
	if !c.cfg.AllowAllUsers {
		if _, ok := c.allowed[message.From.ID]; !ok {
			return nil, admissionUnauthorized
		}
	}
	if !c.allow(message.From.ID) {
		return nil, admissionRateLimited
	}
	var replyText string
	var replyFromBot bool
	if message.ReplyToMessage != nil {
		replyText = TruncateRunes(strings.TrimSpace(message.ReplyToMessage.Text), replyContextRunes)
		replyFromBot = message.ReplyToMessage.From != nil && message.ReplyToMessage.From.ID == bot.ID
	}
	request := &Request{
		MessageID:       message.ID,
		MessageThreadID: message.MessageThreadID,
		UserID:          message.From.ID,
		Username:        message.From.Username,
		FirstName:       message.From.FirstName,
		ChatTitle:       message.Chat.Title,
		Text:            text,
		ReplyText:       replyText,
		ReplyFromBot:    replyFromBot,
	}
	for _, username := range message.Mentions() {
		if !strings.EqualFold(username, bot.Username) {
			request.Mentions = append(request.Mentions, username)
		}
	}
	if message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && !replyFromBot {
		request.ReplyUserID = message.ReplyToMessage.From.ID
		request.ReplyUsername = message.ReplyToMessage.From.Username
		request.ReplyFirstName = message.ReplyToMessage.From.FirstName
	}
	if message.ReplyToMessage != nil {
		for _, username := range message.ReplyToMessage.Mentions() {
			if !strings.EqualFold(username, bot.Username) {
				request.ReplyMentions = append(request.ReplyMentions, username)
			}
		}
	}
	return request, admissionAccepted
}

func (c *Assistant) allow(userID int64) bool {
	return c.allowAt(userID, time.Now())
}

func (c *Assistant) allowAt(userID int64, now time.Time) bool {
	// The lock makes checking and consuming the user and global buckets one
	// decision; concurrent callers cannot spend the same available capacity.
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, user := range c.users {
		if now.Sub(user.lastSeen) >= rateLimitWindow {
			delete(c.users, id)
		}
	}

	user, ok := c.users[userID]
	if !ok {
		user.limiter = hourlyRateLimiter(c.cfg.UserPerHour, c.cfg.UserRequestBurst)
	}
	user.lastSeen = now
	c.users[userID] = user
	if user.limiter.TokensAt(now) < 1 || c.global.TokensAt(now) < 1 {
		return false
	}
	return user.limiter.AllowN(now, 1) && c.global.AllowN(now, 1)
}

func hourlyRateLimiter(requests, burst int) *rate.Limiter {
	refill := rate.Limit(float64(requests) / rateLimitWindow.Seconds())
	return rate.NewLimiter(refill, burst)
}

func (c *Assistant) handle(ctx context.Context, request Request) bool {
	typingCtx, stopTyping := context.WithCancel(ctx)
	typingDone := make(chan struct{})
	go func() {
		defer close(typingDone)
		c.showTyping(typingCtx, request)
	}()

	prepared := c.prepare(ctx, request, time.Now())

	completionStarted := time.Now()
	completion, err := c.completer.Complete(ctx, c.cfg.SystemPrompt, prepared.messages)
	stopTyping()
	<-typingDone
	c.metrics.ObserveModelCompletion("deepseek", c.modelName, time.Since(completionStarted), completion, err)
	if err != nil {
		c.logger.Warn("chat completion failed", "message_id", request.MessageID, "chat", request.ChatTitle, "chat_id", c.cfg.DiscussionChatID, "error", err)
		if ctx.Err() != nil {
			c.discardEmptyConversation(prepared.key)
			return false
		}
		c.sendReply(ctx, request, telegram.TextMessage(c.text.Text(localization.AssistantTemporaryFailure, "I couldn't answer that right now.")))
		c.discardEmptyConversation(prepared.key)
		return ctx.Err() == nil
	}

	text, ok := c.completionText(completion)
	generated := completion.FinishReason == deepseek.FinishStop || completion.FinishReason == deepseek.FinishLength
	if !ok {
		c.logger.Warn("chat completion has unexpected finish reason", "message_id", request.MessageID, "chat", request.ChatTitle, "chat_id", c.cfg.DiscussionChatID, "finish_reason", completion.FinishReason)
		text = c.text.Text(localization.AssistantUnexpectedFailure, "I couldn't answer that right now. Apparently even machines have off days.")
	}
	renderedText := TruncateRunes(prepared.conversation.renderAliases(text), 4096)
	message := telegram.TextMessage(renderedText)
	if generated {
		message = telegram.MarkdownMessage(renderedText)
	}
	if !c.sendReply(ctx, request, message) {
		c.discardEmptyConversation(prepared.key)
		return ctx.Err() == nil
	}
	prepared.conversation.remember(prepared.userAlias, prepared.userMessage, text, time.Now(), c.cfg.HistoryExchanges)
	c.metrics.SetActiveConversations(len(c.history))
	return true
}

func (c *Assistant) prepare(ctx context.Context, request Request, startedAt time.Time) preparedRequest {
	key := conversationKey{chatID: c.cfg.DiscussionChatID, threadID: request.MessageThreadID}
	c.expireConversations(startedAt)
	current := c.history[key]
	if current == nil {
		current = newConversation(c.aliasSeed)
		c.history[key] = current
	}

	messages := current.messages()
	if len(messages) > 0 {
		c.metrics.ObserveContext("history")
	}
	historyEntries := len(messages) / 2

	userAlias := current.participantAlias(request.UserID, request.Username, request.FirstName)
	if request.ReplyUserID != 0 {
		current.participantAlias(request.ReplyUserID, request.ReplyUsername, request.ReplyFirstName)
	}
	for _, username := range append(request.Mentions, request.ReplyMentions...) {
		current.mentionAlias(username)
	}

	userMessage, hasReplyContext := current.userMessage(c.cfg.Name, request)
	if hasReplyContext {
		c.metrics.ObserveContext("reply")
	}

	externalContext := c.threadContextForRequest(ctx, request)
	messages = append(messages, deepseek.Message{Role: deepseek.RoleUser, Content: formatTelegramCurrentRequest(userAlias, userMessage, historyEntries, hasReplyContext, externalContext)})

	return preparedRequest{
		key:          key,
		conversation: current,
		messages:     messages,
		userAlias:    userAlias,
		userMessage:  userMessage,
	}
}

func (c *Assistant) threadContextForRequest(ctx context.Context, request Request) string {
	if c.threadContext == nil {
		return ""
	}
	text, ok := c.threadContext.ForText(ctx, thread.Request{Text: request.Text, ReplyText: request.ReplyText})
	if ok {
		c.metrics.ObserveContext("ptchan")
	}
	return text
}

func (c *Assistant) discardEmptyConversation(key conversationKey) {
	conversation := c.history[key]
	if conversation == nil || len(conversation.exchanges) == 0 {
		delete(c.history, key)
	}
}

func (c *Assistant) expireConversations(now time.Time) {
	for existingKey, conversation := range c.history {
		conversation.expire(now, c.cfg.ConversationTTL)
		if len(conversation.exchanges) == 0 {
			delete(c.history, existingKey)
			continue
		}
	}
	c.metrics.SetActiveConversations(len(c.history))
}

func (c *Assistant) showTyping(ctx context.Context, request Request) {
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		if err := c.sender.SendTyping(ctx, c.cfg.DiscussionChatID, request.MessageThreadID); err != nil && ctx.Err() == nil {
			c.logger.Debug("send typing action failed", "message_id", request.MessageID, "chat", request.ChatTitle, "chat_id", c.cfg.DiscussionChatID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Assistant) replyToRejection(ctx context.Context, message *telegram.IncomingMessage, result admissionResult) {
	var text string
	switch result {
	case admissionTooLong:
		text = c.text.Text(localization.AssistantTooLong, "That message is too long. A little restraint won't kill you.")
	case admissionRateLimited:
		text = c.text.Text(localization.AssistantRateLimited, "Slow down. I can only endure so much of you at once.")
	default:
		return
	}
	if !c.replies.Allow() {
		return
	}

	err := c.sender.Send(ctx, telegram.SendRequest{
		ChatID:           c.cfg.DiscussionChatID,
		Message:          telegram.TextMessage(text),
		ReplyToMessageID: message.ID,
		MessageThreadID:  message.MessageThreadID,
	})
	if err != nil && ctx.Err() == nil {
		c.logger.Warn("send chat rejection failed", "message_id", message.ID, "chat", message.Chat.Title, "chat_id", c.cfg.DiscussionChatID, "error", err)
	}
}

func (c *Assistant) sendReply(ctx context.Context, request Request, message telegram.OutgoingMessage) bool {
	err := c.sender.Send(ctx, telegram.SendRequest{
		ChatID:           c.cfg.DiscussionChatID,
		Message:          message,
		ReplyToMessageID: request.MessageID,
		MessageThreadID:  request.MessageThreadID,
	})
	if err != nil {
		c.metrics.ObserveReply(ResultError)
		c.logger.Warn("send chat reply failed", "message_id", request.MessageID, "chat", request.ChatTitle, "chat_id", c.cfg.DiscussionChatID, "error", err)
		return false
	}

	c.metrics.ObserveReply(ResultSuccess)
	c.logger.Info("chat message answered", "message_id", request.MessageID, "chat", request.ChatTitle, "chat_id", c.cfg.DiscussionChatID, "user_id", request.UserID)
	return true
}

func (c *Assistant) completionText(completion deepseek.Completion) (string, bool) {
	switch completion.FinishReason {
	case deepseek.FinishStop, deepseek.FinishLength:
		return completion.Text, completion.Text != ""
	case deepseek.FinishContentFilter:
		return c.text.Text(localization.AssistantFiltered, "I can't help with that request. Yes, even I have standards."), true
	default:
		return "", false
	}
}
