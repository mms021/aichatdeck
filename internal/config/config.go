// Package config loads server configuration from the environment.
package config

import "os"

type Config struct {
	Addr                  string // HTTP listen address
	MetricsAddr           string // Prometheus /metrics listen address; kept off the public API
	GRPCAddr              string
	DatabaseURL           string // PostgreSQL connection URL
	RedisAddr             string
	KafkaBrokers          string // comma-separated; empty disables the outbox relay
	KafkaTopic            string
	TelegramBotToken      string
	TelegramChatID        string
	TelegramUserID        string
	TelegramWebhookSecret string
}

func Load() Config {
	return Config{
		Addr:                  getenv("ADDR", ":47080"),
		MetricsAddr:           getenv("METRICS_ADDR", ":47090"),
		GRPCAddr:              getenv("GRPC_ADDR", ":47051"),
		DatabaseURL:           getenv("DATABASE_URL", "postgres://aichatdeck:aichatdeck@localhost:47432/aichatdeck?sslmode=disable"),
		RedisAddr:             getenv("REDIS_ADDR", "localhost:47379"),
		KafkaBrokers:          getenv("KAFKA_BROKERS", "localhost:47092"),
		KafkaTopic:            getenv("KAFKA_TOPIC", "aichatdeck.task-events"),
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
