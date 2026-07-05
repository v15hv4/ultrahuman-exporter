package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	maxFetchAttempts = 5
	initialBackoff   = time.Second
	maxBackoff       = time.Minute
)

type app struct {
	cfg      config
	db       *sql.DB
	client   *http.Client
	exporter sdkmetric.Exporter
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

	a.stats.LastSuccess = lastSuccess

	for {
		end := time.Now().Unix()
		start := min(lastSuccess+1, end)
		if start >= end {
			return a.exportExporterMetrics(ctx)
		}

		windowEnd := min(start+int64(a.cfg.FetchWindow.Seconds())-1, end)
		resp, err := a.fetchMetricsWithBackoff(ctx, start, windowEnd)
		if err != nil {
			_ = a.exportExporterMetrics(ctx)
			return err
		}

		healthMetrics, exported := buildHealthMetrics(resp)
		if exported == 0 {
			log.Printf("poll completed with no data: start_epoch=%d end_epoch=%d", start, windowEnd)
			return a.exportExporterMetrics(ctx)
		}

		if err := a.exportMetrics(ctx, healthMetrics); err != nil {
			return err
		}
		a.stats.MetricsExported += uint64(exported)

		if err := saveLastSuccessEpoch(a.db, windowEnd); err != nil {
			return err
		}
		lastSuccess = windowEnd
		a.stats.LastSuccess = windowEnd

		log.Printf("poll window completed: start_epoch=%d end_epoch=%d metrics_exported=%d", start, windowEnd, exported)
		if windowEnd >= end {
			return a.exportExporterMetrics(ctx)
		}
	}

}

func (a *app) fetchMetricsWithBackoff(ctx context.Context, start, end int64) (apiResponse, error) {
	backoff := initialBackoff
	for attempt := 1; ; attempt++ {
		started := time.Now()
		resp, statusCode, err := a.fetchMetrics(ctx, start, end)
		a.recordAPIAttempt(statusCode, time.Since(started), err)
		if err == nil {
			return resp, nil
		}

		if attempt >= maxFetchAttempts {
			return apiResponse{}, err
		}

		log.Printf("ultrahuman API call failed; retrying: start_epoch=%d end_epoch=%d attempt=%d status=%s backoff=%s error=%v", start, end, attempt, statusLabel(statusCode, err), backoff, err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return apiResponse{}, ctx.Err()
		case <-timer.C:
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

}

func (a *app) recordAPIAttempt(statusCode string, latency time.Duration, err error) {
	a.stats.APIRequests++
	a.stats.LatencySeconds = append(a.stats.LatencySeconds, latency.Seconds())
	if len(a.stats.LatencySeconds) > 1000 {
		a.stats.LatencySeconds = a.stats.LatencySeconds[len(a.stats.LatencySeconds)-1000:]
	}

	if err != nil {
		a.stats.APIErrors[statusLabel(statusCode, err)]++
	}

}

func statusLabel(statusCode string, err error) string {
	if statusCode != "" {
		if _, parseErr := strconv.Atoi(statusCode); parseErr == nil {
			return statusCode
		}
		return statusCode
	}
	if err != nil {
		return "transport"
	}
	return "unknown"
}
