package httpapi

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/yosuang/philo/server/internal/migrate"
	"github.com/yosuang/philo/server/internal/store"
)

var (
	adminDSN  string
	adminPool *pgxpool.Pool
	pgImage   testcontainers.Container
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "philo",
			"POSTGRES_PASSWORD": "philo",
			"POSTGRES_DB":       "philo",
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithDeadline(90 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		os.Exit(1)
	}
	pgImage = container

	host, err := container.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres host: %v\n", err)
		os.Exit(1)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres port: %v\n", err)
		os.Exit(1)
	}
	adminDSN = fmt.Sprintf("postgres://philo:philo@%s:%s/philo?sslmode=disable", host, port.Port())
	adminPool, err = store.Connect(ctx, adminDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin pool: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	adminPool.Close()
	termCtx, termCancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = container.Terminate(termCtx)
	termCancel()
	os.Exit(code)
}

func newDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	name := "t" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx := context.Background()
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	pool, err := store.Connect(ctx, databaseDSN(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func databaseDSN(name string) string {
	u, err := url.Parse(adminDSN)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}
