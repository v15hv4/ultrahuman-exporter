package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("failed to load .env: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	db, err := openStateDB(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exporter, err := newOTLPExporter(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := exporter.Shutdown(shutdownCtx); err != nil {
			log.Printf("failed to shut down OTLP exporter: %v", err)
		}
	}()

	app := &app{
		cfg:      cfg,
		db:       db,
		client:   &http.Client{Timeout: 30 * time.Second},
		exporter: exporter,
		resource: resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName)),
		stats:    exporterStats{APIErrors: map[string]uint64{}},
	}

	if err := app.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
