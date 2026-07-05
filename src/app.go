package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/resource"
)

type app struct {
	cfg      config
	db       *sql.DB
	client   *http.Client
	exporter *otlpmetricgrpc.Exporter
	resource *resource.Resource
	stats    exporterStats
}

func (a *app) run(ctx context.Context) error {
	if err := a.poll(ctx); err != nil {
		log.Printf("poll failed: %v", err)
	}

	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.poll(ctx); err != nil {
				log.Printf("poll failed: %v", err)
			}
		}
	}
}

func (a *app) poll(ctx context.Context) error {
	lastSuccess, err := loadLastSuccessEpoch(a.db)
	if err != nil {
		return err
	}

	end := time.Now().Unix()
	start := min(lastSuccess+1, end)
	a.stats.LastSuccess = lastSuccess

	started := time.Now()
	resp, statusCode, err := a.fetchMetrics(ctx, start, end)
	latency := time.Since(started).Seconds()

	a.stats.APIRequests++
	a.stats.LatencySeconds = append(a.stats.LatencySeconds, latency)
	if len(a.stats.LatencySeconds) > 1000 {
		a.stats.LatencySeconds = a.stats.LatencySeconds[len(a.stats.LatencySeconds)-1000:]
	}

	if err != nil {
		status := statusCode
		if status == "" {
			status = "transport"
		}
		a.stats.APIErrors[status]++
		_ = a.exportExporterMetrics(ctx)
		return err
	}

	healthMetrics, exported := buildHealthMetrics(resp)

	if exported > 0 {
		if err := a.exportMetrics(ctx, healthMetrics); err != nil {
			return err
		}
		a.stats.MetricsExported += uint64(exported)
	}

	if err := saveLastSuccessEpoch(a.db, end); err != nil {
		return err
	}
	a.stats.LastSuccess = end

	if err := a.exportExporterMetrics(ctx); err != nil {
		return err
	}

	log.Printf("poll completed: start_epoch=%d end_epoch=%d metrics_exported=%d", start, end, exported)
	return nil
}
