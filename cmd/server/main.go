// Command server runs the aichatdeck Core Loop MVP: agent registration,
// discovery/manifest, the messaging envelope, and task claim/verify.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aichatdeck/internal/bus"
	"aichatdeck/internal/config"
	"aichatdeck/internal/db"
	"aichatdeck/internal/events"
	"aichatdeck/internal/httpapi"
	"aichatdeck/internal/metrics"
	"aichatdeck/internal/notify"
	"aichatdeck/internal/store"
)

func main() {
	cfg := config.Load()

	sqlDB, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer sqlDB.Close()

	metrics.RegisterDB(sqlDB.DB)

	redisBus := bus.NewRedis(cfg.RedisAddr)
	defer redisBus.Close()

	if err := redisBus.Ping(context.Background()); err != nil {
		log.Fatalf("connect redis at %s: %v", cfg.RedisAddr, err)
	}

	server := &httpapi.Server{
		Agents:           store.NewAgentStore(sqlDB),
		Credentials:      store.NewCredentialStore(sqlDB),
		Tasks:            store.NewTaskStore(sqlDB),
		Messages:         store.NewMessageStore(sqlDB),
		Knowledge:        store.NewKnowledgeStore(sqlDB),
		Artifacts:        store.NewArtifactStore(sqlDB),
		Projects:         store.NewProjectStore(sqlDB),
		Members:          store.NewMembershipStore(sqlDB),
		OperatorRequests: store.NewOperatorRequestStore(sqlDB),
		Bus:              redisBus,
		DB:               sqlDB,
		RedisPinger:      redisBus,
		Logger:           log.Default(),
	}
	server.TelegramUserID = cfg.TelegramUserID
	server.TelegramWebhookSecret = cfg.TelegramWebhookSecret
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		server.OperatorNotifier = &notify.Telegram{Token: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID}
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.NewRouter(server),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	alertsDone := make(chan struct{})
	go func() { defer close(alertsDone); server.RunVerificationAlerts(ctx) }()

	relayDone := make(chan struct{})
	if cfg.KafkaBrokers == "" {
		close(relayDone)
		log.Println("KAFKA_BROKERS empty: task events stay in the outbox")
	} else {
		kafka, err := events.NewKafka(strings.Split(cfg.KafkaBrokers, ","), cfg.KafkaTopic)
		if err != nil {
			log.Fatalf("kafka client: %v", err)
		}
		defer kafka.Close()
		relay := &events.Relay{DB: sqlDB, Publisher: kafka, Logger: log.Default()}
		go func() { defer close(relayDone); relay.Run(ctx) }()
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
	metricsServer := &http.Server{Addr: cfg.MetricsAddr, Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve metrics: %v", err)
		}
	}()

	go func() {
		log.Printf("aichatdeck listening on %s (redis=%s, kafka=%s, metrics on %s)", cfg.Addr, cfg.RedisAddr, cfg.KafkaBrokers, cfg.MetricsAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	_ = metricsServer.Shutdown(shutdownCtx)
	<-alertsDone
	<-relayDone
}
