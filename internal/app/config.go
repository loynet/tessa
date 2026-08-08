package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/loynet/ptchan-ai/context/thread"
	"github.com/pelletier/go-toml/v2"

	"tessa/internal/chat"
	"tessa/internal/localization"
)

type Config struct {
	Locale        localization.Locale
	Ptchan        PtchanConfig
	Telegram      TelegramConfig
	Chat          chat.Config
	ThreadContext thread.Config
	DeepSeek      DeepSeekConfig
	Runtime       RuntimeConfig
	SQLitePath    string
}

type PtchanConfig struct {
	BaseURL         string
	GatewayURL      string
	IntegrationName string
	Secret          string
}

type TelegramConfig struct {
	BotToken string
}

type DeepSeekConfig struct {
	APIKey    string
	Model     string
	MaxTokens int
}

type RuntimeConfig struct {
	HTTPAddr string
	Logging  LoggingConfig
}

type LoggingConfig struct {
	Level slog.Level
}

type fileConfig struct {
	Locale string `toml:"locale"`
	Name   string `toml:"name"`
	Ptchan struct {
		BaseURL         string `toml:"base_url"`
		GatewayURL      string `toml:"gateway_url"`
		IntegrationName string `toml:"integration_name"`
	} `toml:"ptchan"`
	Telegram struct {
		DiscussionChatID int64   `toml:"discussion_chat_id"`
		AllowedUserIDs   []int64 `toml:"allowed_user_ids"`
		AllowAllUsers    bool    `toml:"allow_all_users"`
	} `toml:"telegram"`
	Chat struct {
		MaxInputRunes int    `toml:"max_input_runes"`
		SystemPrompt  string `toml:"system_prompt"`
		RateLimit     struct {
			UserPerHour   int `toml:"user_per_hour"`
			UserBurst     int `toml:"user_burst"`
			GlobalPerHour int `toml:"global_per_hour"`
			GlobalBurst   int `toml:"global_burst"`
		} `toml:"rate_limit"`
		Memory struct {
			TTL              string `toml:"ttl"`
			HistoryExchanges int    `toml:"history_exchanges"`
		} `toml:"memory"`
		PtchanContext struct {
			MaxReplies int `toml:"max_replies"`
		} `toml:"ptchan_context"`
	} `toml:"chat"`
	DeepSeek struct {
		Model     string `toml:"model"`
		MaxTokens int    `toml:"max_tokens"`
	} `toml:"deepseek"`
	Runtime struct {
		HTTPAddr string `toml:"http_addr"`
		Logging  struct {
			Level string `toml:"level"`
		} `toml:"logging"`
	} `toml:"runtime"`
	Storage struct {
		SQLitePath string `toml:"sqlite_path"`
	} `toml:"storage"`
}

func LoadConfig() (Config, error) {
	path := strings.TrimSpace(os.Getenv("CONFIG_FILE"))
	if path == "" {
		return Config{}, fmt.Errorf("CONFIG_FILE is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()
	var raw fileConfig
	d := toml.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return Config{}, fmt.Errorf("decode config %q:\n%s", path, strict.String())
		}
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	locale, err := localization.Parse(strings.TrimSpace(raw.Locale))
	if err != nil {
		return Config{}, err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(raw.Runtime.Logging.Level))); err != nil {
		return Config{}, fmt.Errorf("runtime.logging.level must be debug, info, warn, or error")
	}
	ttl, err := time.ParseDuration(raw.Chat.Memory.TTL)
	if err != nil || ttl <= 0 {
		return Config{}, fmt.Errorf("chat.memory.ttl must be a positive duration")
	}
	ptchan := PtchanConfig{
		BaseURL:         strings.TrimRight(strings.TrimSpace(raw.Ptchan.BaseURL), "/"),
		GatewayURL:      strings.TrimRight(strings.TrimSpace(raw.Ptchan.GatewayURL), "/"),
		IntegrationName: strings.TrimSpace(raw.Ptchan.IntegrationName),
	}
	ptchan.Secret = strings.TrimSpace(os.Getenv(integrationSecretEnv(ptchan.IntegrationName)))
	name := strings.TrimSpace(raw.Name)
	cfg := Config{
		Locale:   locale,
		Ptchan:   ptchan,
		Telegram: TelegramConfig{BotToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))},
		DeepSeek: DeepSeekConfig{
			APIKey:    strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")),
			Model:     strings.TrimSpace(raw.DeepSeek.Model),
			MaxTokens: raw.DeepSeek.MaxTokens,
		},
		Runtime: RuntimeConfig{
			HTTPAddr: strings.TrimSpace(raw.Runtime.HTTPAddr),
			Logging:  LoggingConfig{Level: level},
		},
		SQLitePath: filepath.Clean(strings.TrimSpace(raw.Storage.SQLitePath)),
		Chat: chat.Config{
			Name:               name,
			DiscussionChatID:   raw.Telegram.DiscussionChatID,
			AllowAllUsers:      raw.Telegram.AllowAllUsers,
			AllowedUserIDs:     raw.Telegram.AllowedUserIDs,
			UserPerHour:        raw.Chat.RateLimit.UserPerHour,
			UserRequestBurst:   raw.Chat.RateLimit.UserBurst,
			GlobalPerHour:      raw.Chat.RateLimit.GlobalPerHour,
			GlobalRequestBurst: raw.Chat.RateLimit.GlobalBurst,
			SystemPrompt:       strings.ReplaceAll(strings.TrimSpace(raw.Chat.SystemPrompt), "{{name}}", name),
			MaxInputRunes:      raw.Chat.MaxInputRunes,
			ConversationTTL:    ttl,
			HistoryExchanges:   raw.Chat.Memory.HistoryExchanges,
		},
		ThreadContext: thread.Config{
			BaseURL:         ptchan.BaseURL,
			IntegrationName: ptchan.IntegrationName,
			MaxReplies:      raw.Chat.PtchanContext.MaxReplies,
		},
	}
	return cfg, cfg.ValidateRun()
}
func (c Config) ValidateRun() error {
	if c.SQLitePath == "" || c.SQLitePath == "." {
		return fmt.Errorf("storage.sqlite_path is required")
	}
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if c.Chat.Name == "" || c.Chat.SystemPrompt == "" || c.Chat.DiscussionChatID == 0 {
		return fmt.Errorf("name, chat.system_prompt, and telegram.discussion_chat_id are required")
	}
	if !c.Chat.AllowAllUsers && len(c.Chat.AllowedUserIDs) == 0 {
		return fmt.Errorf("telegram.allowed_user_ids requires at least one user")
	}
	if c.DeepSeek.APIKey == "" || c.DeepSeek.Model == "" || c.DeepSeek.MaxTokens <= 0 {
		return fmt.Errorf("DEEPSEEK_API_KEY, deepseek.model, and deepseek.max_tokens are required")
	}
	if c.Ptchan.BaseURL == "" || c.Ptchan.GatewayURL == "" || c.Ptchan.IntegrationName == "" || c.Ptchan.Secret == "" {
		return fmt.Errorf("ptchan base URL, gateway URL, integration name, and secret are required")
	}
	if c.Chat.MaxInputRunes <= 0 || c.Chat.HistoryExchanges <= 0 || c.ThreadContext.MaxReplies <= 0 {
		return fmt.Errorf("chat limits must be positive")
	}
	if c.Chat.UserPerHour <= 0 || c.Chat.UserRequestBurst <= 0 || c.Chat.UserRequestBurst > c.Chat.UserPerHour {
		return fmt.Errorf("chat.rate_limit.user_burst must be positive and no greater than user_per_hour")
	}
	if c.Chat.GlobalPerHour <= 0 || c.Chat.GlobalRequestBurst <= 0 || c.Chat.GlobalRequestBurst > c.Chat.GlobalPerHour {
		return fmt.Errorf("chat.rate_limit.global_burst must be positive and no greater than global_per_hour")
	}
	return nil
}
func integrationSecretEnv(name string) string {
	return "PTCHAN_INTEGRATION_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name)) + "_SECRET"
}
