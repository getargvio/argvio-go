package argvio

import (
	"context"
	"runtime/debug"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// sdkVersion returns this module's own version, read from build info at
// runtime rather than hardcoded, so it always matches the semver tag a
// vendor actually pinned in their go.mod.
func sdkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == modulePath {
			return dep.Version
		}
	}
	if bi.Main.Path == modulePath && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "unknown"
}

const modulePath = "github.com/getargvio/argvio-go"

func buildResource(cfg *config) *resource.Resource {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.cliName),
		semconv.ServiceVersion(cfg.cliVersion),
		attribute.String("telemetry.sdk.name", "argvio-go"),
		attribute.String("telemetry.sdk.version", sdkVersion()),
		attribute.String("telemetry.sdk.language", "go"),
	}
	for _, kv := range cfg.extraResourceAttrs {
		attrs = append(attrs, attribute.String(kv.key, kv.value))
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(attrs...),
	)
	if err != nil || res == nil {
		return resource.NewSchemaless(attrs...)
	}
	return res
}
