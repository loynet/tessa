package app

import (
	"strings"
	"testing"

	"github.com/loynet/ptchan-ai/context/thread"
	"tessa/internal/chat"
)

func TestValidateRunRejectsInvalidRateLimits(t *testing.T) {
	cfg := Config{SQLitePath: "data/tessa.db", Telegram: TelegramConfig{BotToken: "token"}, Ptchan: PtchanConfig{BaseURL: "https://ptchan.org", GatewayURL: "https://gateway.example", IntegrationName: "tessa", Secret: "secret"}, DeepSeek: DeepSeekConfig{APIKey: "key", Model: "model", MaxTokens: 1}, Chat: chat.Config{Name: "Marta", SystemPrompt: "prompt", DiscussionChatID: 1, AllowAllUsers: true, MaxInputRunes: 1, HistoryExchanges: 1}, ThreadContext: thread.Config{MaxReplies: 1}}
	cfg.Chat.UserPerHour, cfg.Chat.UserRequestBurst = 1, 0
	if err := cfg.ValidateRun(); err == nil || !strings.Contains(err.Error(), "user_burst") {
		t.Fatalf("ValidateRun() = %v", err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("CONFIG_FILE", "../../config/example.toml")
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("DEEPSEEK_API_KEY", "key")
	t.Setenv("PTCHAN_INTEGRATION_TESSA_SECRET", "secret")
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig() = %v", err)
	}
}
