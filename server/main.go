package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/httpapi"
	"github.com/yosuang/philo/server/internal/migrate"
	"github.com/yosuang/philo/server/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := config.FromEnv()
	if cfg.DatabaseURL == "" {
		log.Fatal("PHILO_DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %s", safeDBErr(err))
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		log.Fatalf("migrate: %s", safeDBErr(err))
	}

	app, err := httpapi.New(pool, cfg)
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		app.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("philo session server listening on %s adapter=%s", cfg.HTTPAddr, cfg.Adapter)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	var last error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err := store.Connect(ctx, databaseURL)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		last = err
		log.Printf("database is not ready (attempt %d): %s", attempt, safeDBErr(err))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, last
}

func safeDBErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "password") {
		return "connection failed"
	}
	return msg
}
