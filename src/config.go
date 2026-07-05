package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL       = "https://partner.ultrahuman.com/api/v1/partner/daily_metrics"
	defaultPollInterval = 5 * time.Minute
	defaultFetchWindow  = 24 * time.Hour
	defaultDBPath       = "ultrahuman-exporter.db"
	serviceName         = "ultrahuman-exporter"
)

type config struct {
	APIKey       string
	APIURL       string
	PollInterval time.Duration
	FetchWindow  time.Duration
	DBPath       string
	OTLPEndpoint string
	OTLPInsecure bool
}

func loadConfig() (config, error) {
	apiKey := strings.TrimSpace(os.Getenv("UH_API_KEY"))
	if apiKey == "" {
		return config{}, errors.New("UH_API_KEY is required")
	}

	interval := defaultPollInterval
	if raw := strings.TrimSpace(os.Getenv("UH_POLL_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid UH_POLL_INTERVAL: %w", err)
		}
		interval = parsed
	}

	apiURL := strings.TrimSpace(os.Getenv("UH_API_URL"))
	if apiURL == "" {
		apiURL = defaultAPIURL
	}

	fetchWindow := defaultFetchWindow
	if raw := strings.TrimSpace(os.Getenv("UH_FETCH_WINDOW")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid UH_FETCH_WINDOW: %w", err)
		}
		if parsed < time.Second {
			return config{}, errors.New("UH_FETCH_WINDOW must be at least 1s")
		}
		fetchWindow = parsed
	}

	dbPath := strings.TrimSpace(os.Getenv("UH_DB_PATH"))
	if dbPath == "" {
		dbPath = defaultDBPath
	}

	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if otlpEndpoint == "" {
		otlpEndpoint = "http://localhost:4318/v1/metrics"
	}
	if !strings.HasPrefix(otlpEndpoint, "http://") && !strings.HasPrefix(otlpEndpoint, "https://") {
		otlpEndpoint = "http://" + otlpEndpoint
	}

	insecure, err := strconv.ParseBool(defaultString(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true"))
	if err != nil {
		return config{}, fmt.Errorf("invalid OTEL_EXPORTER_OTLP_INSECURE: %w", err)
	}

	return config{
		APIKey:       apiKey,
		APIURL:       apiURL,
		PollInterval: interval,
		FetchWindow:  fetchWindow,
		DBPath:       dbPath,
		OTLPEndpoint: otlpEndpoint,
		OTLPInsecure: insecure,
	}, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
