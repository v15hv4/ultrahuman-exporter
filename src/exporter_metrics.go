package main

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type exporterStats struct {
	APIRequests     uint64
	APIErrors       map[string]uint64
	LatencySeconds  []float64
	LastSuccess     int64
	MetricsExported uint64
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
