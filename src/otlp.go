package main

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
)

func newOTLPExporter(ctx context.Context, cfg config) (*otlpmetrichttp.Exporter, error) {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(cfg.OTLPEndpoint)}
	if cfg.OTLPInsecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	return otlpmetrichttp.New(ctx, opts...)
}
