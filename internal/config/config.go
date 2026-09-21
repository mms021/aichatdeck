// Package config loads server configuration from the environment.
package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	Addr                  string // HTTP listen address
	SQLitePath            string
	RedisAddr             string
	TelegramBotToken      string
	TelegramChatID        string
	TelegramUserID        string
	TelegramWebhookSecret string
}

func Load() Config {
	return Config{
		Addr:                  getenv("ADDR", ":8080"),
		SQLitePath:            getenv("SQLITE_PATH", defaultSQLitePath()),
		RedisAddr:             getenv("REDIS_ADDR", "localhost:6379"),
		TelegramBotToken:      getenv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:        getenv("TELEGRAM_CHAT_ID", ""),
		TelegramUserID:        getenv("TELEGRAM_USER_ID", ""),
		TelegramWebhookSecret: getenv("TELEGRAM_WEBHOOK_SECRET", ""),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultSQLitePath is absolute so the database, and with it every agent
// identity, does not depend on which directory the server was started from.
func defaultSQLitePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "aichatdeck.db"
	}
	return filepath.Join(home, ".aichatdeck", "aichatdeck.db")
}
