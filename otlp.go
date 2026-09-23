package argvio

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
)

// exporters bundles the three signal-specific OTLP exporters built for
// a single Client. Each is independently constructed so a failure in
// one (e.g. an environment that blocks gRPC but allows HTTP is
// misconfigured for gRPC) doesn't necessarily prevent the others.
type exporters struct {
	trace  sdktrace.SpanExporter
	metric sdkmetric.Exporter
	log    sdklog.Exporter
}

func buildExporters(ctx context.Context, cfg *config) (*exporters, error) {
	headers := map[string]string{cfg.authHeader: cfg.apiKey}

	var (
		te  sdktrace.SpanExporter
		me  sdkmetric.Exporter
		le  sdklog.Exporter
		err error
	)

	if cfg.useHTTP {
		te, err = newTraceExporterHTTP(ctx, cfg, headers)
		if err != nil {
			return nil, fmt.Errorf("argvio: trace exporter: %w", err)
		}
		me, err = newMetricExporterHTTP(ctx, cfg, headers)
		if err != nil {
			return nil, fmt.Errorf("argvio: metric exporter: %w", err)
		}
		le, err = newLogExporterHTTP(ctx, cfg, headers)
		if err != nil {
			return nil, fmt.Errorf("argvio: log exporter: %w", err)
		}
		return &exporters{trace: te, metric: me, log: le}, nil
	}

	te, err = newTraceExporterGRPC(ctx, cfg, headers)
	if err != nil {
		return nil, fmt.Errorf("argvio: trace exporter: %w", err)
	}
	me, err = newMetricExporterGRPC(ctx, cfg, headers)
	if err != nil {
		return nil, fmt.Errorf("argvio: metric exporter: %w", err)
	}
	le, err = newLogExporterGRPC(ctx, cfg, headers)
	if err != nil {
		return nil, fmt.Errorf("argvio: log exporter: %w", err)
	}
	return &exporters{trace: te, metric: me, log: le}, nil
}

func newTraceExporterGRPC(ctx context.Context, cfg *config, headers map[string]string) (*otlptrace.Exporter, error) {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithHeaders(headers),
		otlptracegrpc.WithTimeout(cfg.exportTimeout),
	}
	if hasScheme(cfg.endpoint) {
		opts = append(opts, otlptracegrpc.WithEndpointURL(cfg.endpoint))
	} else {
		opts = append(opts, otlptracegrpc.WithEndpoint(cfg.endpoint))
	}
	if cfg.insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return otlptracegrpc.New(ctx, opts...)
}

func newTraceExporterHTTP(ctx context.Context, cfg *config, headers map[string]string) (*otlptrace.Exporter, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(cfg.exportTimeout),
	}
	if hasScheme(cfg.endpoint) {
		opts = append(opts, otlptracehttp.WithEndpointURL(strings.TrimSuffix(cfg.endpoint, "/")+"/v1/traces"))
	} else {
		opts = append(opts, otlptracehttp.WithEndpoint(cfg.endpoint))
	}
	if cfg.insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	return otlptracehttp.New(ctx, opts...)
}

func newMetricExporterGRPC(ctx context.Context, cfg *config, headers map[string]string) (sdkmetric.Exporter, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithHeaders(headers),
		otlpmetricgrpc.WithTimeout(cfg.exportTimeout),
	}
	if hasScheme(cfg.endpoint) {
		opts = append(opts, otlpmetricgrpc.WithEndpointURL(cfg.endpoint))
	} else {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(cfg.endpoint))
	}
	if cfg.insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return otlpmetricgrpc.New(ctx, opts...)
}

func newMetricExporterHTTP(ctx context.Context, cfg *config, headers map[string]string) (sdkmetric.Exporter, error) {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithHeaders(headers),
		otlpmetrichttp.WithTimeout(cfg.exportTimeout),
	}
	if hasScheme(cfg.endpoint) {
		opts = append(opts, otlpmetrichttp.WithEndpointURL(strings.TrimSuffix(cfg.endpoint, "/")+"/v1/metrics"))
	} else {
		opts = append(opts, otlpmetrichttp.WithEndpoint(cfg.endpoint))
	}
	if cfg.insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	return otlpmetrichttp.New(ctx, opts...)
}

func newLogExporterGRPC(ctx context.Context, cfg *config, headers map[string]string) (sdklog.Exporter, error) {
	opts := []otlploggrpc.Option{
		otlploggrpc.WithHeaders(headers),
		otlploggrpc.WithTimeout(cfg.exportTimeout),
	}
	if hasScheme(cfg.endpoint) {
		opts = append(opts, otlploggrpc.WithEndpointURL(cfg.endpoint))
	} else {
		opts = append(opts, otlploggrpc.WithEndpoint(cfg.endpoint))
	}
	if cfg.insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}
	return otlploggrpc.New(ctx, opts...)
}

func newLogExporterHTTP(ctx context.Context, cfg *config, headers map[string]string) (sdklog.Exporter, error) {
	opts := []otlploghttp.Option{
		otlploghttp.WithHeaders(headers),
		otlploghttp.WithTimeout(cfg.exportTimeout),
	}
	if hasScheme(cfg.endpoint) {
		opts = append(opts, otlploghttp.WithEndpointURL(strings.TrimSuffix(cfg.endpoint, "/")+"/v1/logs"))
	} else {
		opts = append(opts, otlploghttp.WithEndpoint(cfg.endpoint))
	}
	if cfg.insecure {
		opts = append(opts, otlploghttp.WithInsecure())
	}
	return otlploghttp.New(ctx, opts...)
}

// hasScheme reports whether endpoint is a URL (e.g. "https://host:4318",
// the form OTEL_EXPORTER_OTLP_ENDPOINT uses) rather than a bare
// host:port. URLs go through the exporters' WithEndpointURL, which
// derives TLS from the scheme; for HTTP the per-signal path is appended
// to the base URL, as the OTel spec prescribes for the generic
// endpoint variable.
func hasScheme(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
