package main

import (
	"context"
	"log"
	"time"

	"github.com/ArminDashti/centralized-agent-data-api/internal/auth"
	"github.com/ArminDashti/centralized-agent-data-api/internal/config"
	appdb "github.com/ArminDashti/centralized-agent-data-api/internal/db"
	"github.com/ArminDashti/centralized-agent-data-api/internal/handlers"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	config.LoadDotEnv(".env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	sqlDB, err := appdb.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer sqlDB.Close()

	if err := appdb.Migrate(sqlDB, cfg.MigrationsDir); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hash, err := auth.HashPassword(appdb.DefaultPassword)
	if err != nil {
		log.Fatalf("hash default password: %v", err)
	}
	if err := appdb.SeedDefaultUser(ctx, sqlDB, hash); err != nil {
		log.Fatalf("seed default user: %v", err)
	}

	h := handlers.New(sqlDB, cfg)
	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowOriginFunc:  func(string) bool { return true },
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	r.GET("/health", h.Health)
	api := r.Group("/api/v1")
	{
		api.POST("/auth/login", h.Login)
		authed := api.Group("")
		authed.Use(auth.Middleware(cfg.JWTSecret))
		{
			authed.POST("/sessions/ingest", h.IngestSession)
			authed.GET("/sessions", h.ListSessions)
			authed.GET("/sessions/:uuid", h.GetSession)
			authed.GET("/sessions/:uuid/thinking", h.GetThinking)
			authed.GET("/sessions/:uuid/turns", h.GetTurns)
		}
	}

	log.Printf("listening on %s", cfg.Addr)
	if err := r.Run(cfg.Addr); err != nil {
		log.Fatal(err)
	}
}
