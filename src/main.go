package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

const (
	stateKeyLastSuccess = "last_success_epoch"
	defaultAPIURL       = "https://partner.ultrahuman.com/api/v1/partner/daily_metrics"
	defaultPollInterval = 5 * time.Minute
	defaultDBPath       = "ultrahuman-exporter.db"
	serviceName         = "ultrahuman-exporter"
)

type config struct {
	APIKey       string
	APIURL       string
	PollInterval time.Duration
	DBPath       string
	OTLPEndpoint string
	OTLPInsecure bool
}

type apiResponse struct {
	Data struct {
		Metrics map[string][]apiMetric `json:"metrics"`
	} `json:"data"`
}

type apiMetric struct {
	Type   string       `json:"type"`
	Object metricObject `json:"object"`
}

type metricObject struct {
	DayStartTimestamp int64    `json:"day_start_timestamp"`
	Value             *float64 `json:"value"`
	Values            []sample `json:"values"`
}

type sample struct {
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
}

type metricMapping struct {
	Name        string
	Description string
	Unit        string
	Kind        string
}

type exporterStats struct {
	APIRequests     uint64
	APIErrors       map[string]uint64
	LatencySeconds  []float64
	LastSuccess     int64
	MetricsExported uint64
}

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

func (a *app) fetchMetrics(ctx context.Context, start, end int64) (apiResponse, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.APIURL, nil)
	if err != nil {
		return apiResponse{}, "", err
	}

	q := req.URL.Query()
	q.Set("start_epoch", strconv.FormatInt(start, 10))
	q.Set("end_epoch", strconv.FormatInt(end, 10))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", a.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		return apiResponse{}, "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return apiResponse{}, strconv.Itoa(res.StatusCode), err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiResponse{}, strconv.Itoa(res.StatusCode), fmt.Errorf("ultrahuman API returned status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload apiResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return apiResponse{}, strconv.Itoa(res.StatusCode), err
	}

	return payload, strconv.Itoa(res.StatusCode), nil
}

func buildHealthMetrics(resp apiResponse) ([]metricdata.Metrics, int) {
	seriesPoints := map[string][]metricdata.DataPoint[float64]{}
	sumPoints := map[string][]metricdata.DataPoint[float64]{}

	for date, metrics := range resp.Data.Metrics {
		for _, metric := range metrics {
			mapping, ok := healthMetricMappings[metric.Type]
			if !ok {
				continue
			}

			if len(metric.Object.Values) > 0 {
				for _, value := range metric.Object.Values {
					ts := time.Unix(value.Timestamp, 0)
					point := metricdata.DataPoint[float64]{Attributes: dateAttr(date), Time: ts, Value: value.Value}
					if mapping.Kind == "sum" {
						sumPoints[metric.Type] = append(sumPoints[metric.Type], point)
					} else {
						seriesPoints[metric.Type] = append(seriesPoints[metric.Type], point)
					}
				}
				continue
			}

			if metric.Object.Value == nil {
				continue
			}

			ts := scalarTimestamp(date, metric.Object.DayStartTimestamp)
			seriesPoints[metric.Type] = append(seriesPoints[metric.Type], metricdata.DataPoint[float64]{
				Attributes: dateAttr(date),
				Time:       ts,
				Value:      *metric.Object.Value,
			})
		}
	}

	result := make([]metricdata.Metrics, 0, len(seriesPoints)+len(sumPoints))
	exported := 0
	for apiType, points := range seriesPoints {
		mapping := healthMetricMappings[apiType]
		result = append(result, metricdata.Metrics{
			Name:        mapping.Name,
			Description: mapping.Description,
			Unit:        mapping.Unit,
			Data:        metricdata.Gauge[float64]{DataPoints: points},
		})
		exported += len(points)
	}
	for apiType, points := range sumPoints {
		mapping := healthMetricMappings[apiType]
		result = append(result, metricdata.Metrics{
			Name:        mapping.Name,
			Description: mapping.Description,
			Unit:        mapping.Unit,
			Data: metricdata.Sum[float64]{
				DataPoints:  points,
				Temporality: metricdata.CumulativeTemporality,
				IsMonotonic: true,
			},
		})
		exported += len(points)
	}

	return result, exported
}

func (a *app) exportExporterMetrics(ctx context.Context) error {
	now := time.Now()
	metrics := []metricdata.Metrics{
		{
			Name:        "uhex_api_requests_total",
			Description: "Total Ultrahuman API calls",
			Data: metricdata.Sum[int64]{
				DataPoints:  []metricdata.DataPoint[int64]{{Time: now, Value: int64(a.stats.APIRequests)}},
				Temporality: metricdata.CumulativeTemporality,
				IsMonotonic: true,
			},
		},
		{
			Name:        "uhex_api_latency_seconds",
			Description: "Ultrahuman API response time",
			Unit:        "s",
			Data:        latencyHistogram(a.stats.LatencySeconds, now),
		},
		{
			Name:        "uhex_last_success_epoch",
			Description: "Last successful fetch timestamp",
			Unit:        "s",
			Data:        metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Time: now, Value: a.stats.LastSuccess}}},
		},
		{
			Name:        "uhex_metrics_exported_total",
			Description: "Data points sent",
			Data: metricdata.Sum[int64]{
				DataPoints:  []metricdata.DataPoint[int64]{{Time: now, Value: int64(a.stats.MetricsExported)}},
				Temporality: metricdata.CumulativeTemporality,
				IsMonotonic: true,
			},
		},
	}

	for status, count := range a.stats.APIErrors {
		metrics = append(metrics, metricdata.Metrics{
			Name:        "uhex_api_errors_total",
			Description: "Failed Ultrahuman API calls",
			Data: metricdata.Sum[int64]{
				DataPoints: []metricdata.DataPoint[int64]{{
					Attributes: attribute.NewSet(attribute.String("status_code", status)),
					Time:       now,
					Value:      int64(count),
				}},
				Temporality: metricdata.CumulativeTemporality,
				IsMonotonic: true,
			},
		})
	}

	return a.exportMetrics(ctx, metrics)
}

func latencyHistogram(values []float64, ts time.Time) metricdata.Histogram[float64] {
	bounds := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	counts := make([]uint64, len(bounds)+1)
	var sum float64
	for _, value := range values {
		sum += value
		bucket := len(bounds)
		for i, bound := range bounds {
			if value <= bound {
				bucket = i
				break
			}
		}
		counts[bucket]++
	}

	return metricdata.Histogram[float64]{
		DataPoints: []metricdata.HistogramDataPoint[float64]{{
			Time:         ts,
			Count:        uint64(len(values)),
			Bounds:       bounds,
			BucketCounts: counts,
			Sum:          sum,
		}},
		Temporality: metricdata.CumulativeTemporality,
	}
}

func (a *app) exportMetrics(ctx context.Context, metrics []metricdata.Metrics) error {
	if len(metrics) == 0 {
		return nil
	}

	exportCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return a.exporter.Export(exportCtx, &metricdata.ResourceMetrics{
		Resource: a.resource,
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Scope:   instrumentation.Scope{Name: serviceName},
			Metrics: metrics,
		}},
	})
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

	dbPath := strings.TrimSpace(os.Getenv("UH_DB_PATH"))
	if dbPath == "" {
		dbPath = defaultDBPath
	}

	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if otlpEndpoint == "" {
		otlpEndpoint = "localhost:4317"
	}
	otlpEndpoint = strings.TrimPrefix(otlpEndpoint, "http://")
	otlpEndpoint = strings.TrimPrefix(otlpEndpoint, "https://")

	insecure, err := strconv.ParseBool(defaultString(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true"))
	if err != nil {
		return config{}, fmt.Errorf("invalid OTEL_EXPORTER_OTLP_INSECURE: %w", err)
	}

	return config{
		APIKey:       apiKey,
		APIURL:       apiURL,
		PollInterval: interval,
		DBPath:       dbPath,
		OTLPEndpoint: otlpEndpoint,
		OTLPInsecure: insecure,
	}, nil
}

func newOTLPExporter(ctx context.Context, cfg config) (*otlpmetricgrpc.Exporter, error) {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint)}
	if cfg.OTLPInsecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return otlpmetricgrpc.New(ctx, opts...)
}

func openStateDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func loadLastSuccessEpoch(db *sql.DB) (int64, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM state WHERE key = ?`, stateKeyLastSuccess).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		initial := time.Now().Add(-24 * time.Hour).Unix()
		return initial, saveLastSuccessEpoch(db, initial)
	}
	if err != nil {
		return 0, err
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid stored %s value %q: %w", stateKeyLastSuccess, raw, err)
	}

	return value, nil
}

func saveLastSuccessEpoch(db *sql.DB, epoch int64) error {
	_, err := db.Exec(
		`INSERT INTO state(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		stateKeyLastSuccess,
		strconv.FormatInt(epoch, 10),
	)
	return err
}

func scalarTimestamp(date string, dayStart int64) time.Time {
	if dayStart > 0 {
		return time.Unix(dayStart, 0)
	}

	parsed, err := time.Parse("2006-01-02", date)
	if err == nil {
		return parsed
	}

	return time.Now()
}

func dateAttr(date string) attribute.Set {
	if date == "" {
		return attribute.Set{}
	}
	return attribute.NewSet(attribute.String("date", date))
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

var healthMetricMappings = map[string]metricMapping{
	"hr":                {Name: "uh_heart_rate_bpm", Description: "Heart rate", Unit: "{beats}/min"},
	"temp":              {Name: "uh_skin_temperature_celsius", Description: "Skin temperature", Unit: "Cel"},
	"spo2":              {Name: "uh_spo2_percent", Description: "Blood oxygen saturation", Unit: "%"},
	"hrv":               {Name: "uh_hrv_ms", Description: "Heart rate variability", Unit: "ms"},
	"steps":             {Name: "uh_steps_total", Description: "Steps", Unit: "{steps}", Kind: "sum"},
	"sleep_rhr":         {Name: "uh_resting_heart_rate_bpm", Description: "Resting heart rate", Unit: "{beats}/min"},
	"avg_sleep_hrv":     {Name: "uh_avg_sleep_hrv_ms", Description: "Average sleep heart rate variability", Unit: "ms"},
	"recovery_index":    {Name: "uh_recovery_index", Description: "Recovery index", Unit: "1"},
	"movement_index":    {Name: "uh_movement_index", Description: "Movement index", Unit: "1"},
	"active_minutes":    {Name: "uh_active_minutes", Description: "Active minutes", Unit: "min"},
	"movements":         {Name: "uh_movements_count", Description: "Movements", Unit: "{movements}"},
	"morning_alertness": {Name: "uh_morning_alertness", Description: "Morning alertness", Unit: "1"},
	"deep_sleep":        {Name: "uh_deep_sleep_minutes", Description: "Deep sleep", Unit: "min"},
	"light_sleep":       {Name: "uh_light_sleep_minutes", Description: "Light sleep", Unit: "min"},
	"rem_sleep":         {Name: "uh_rem_sleep_minutes", Description: "REM sleep", Unit: "min"},
	"total_sleep":       {Name: "uh_total_sleep_minutes", Description: "Total sleep", Unit: "min"},
	"sleep_efic":        {Name: "uh_sleep_efficiency_percent", Description: "Sleep efficiency", Unit: "%"},
}
