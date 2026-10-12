package argvio

import (
	"context"
	"runtime"
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
		attribute.String(ResourceAttrCLIVersion, cfg.cliVersion),
		attribute.String(ResourceAttrOS, normalizeOS(runtime.GOOS)),
		attribute.String(ResourceAttrArch, normalizeArch(runtime.GOARCH)),
	}
	attrs = append(attrs, cfg.ci.resourceAttributes()...)
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

// Resource attribute keys describing the CLI build and host, promoted by
// the server into filterable columns.
const (
	ResourceAttrCLIVersion = "cli.version"
	ResourceAttrOS         = "cli.os"
	ResourceAttrArch       = "cli.arch"
)

// normalizeOS maps runtime.GOOS onto the server's cli.os enum.
func normalizeOS(goos string) string {
	switch goos {
	case "linux", "darwin", "windows":
		return goos
	default:
		return "other"
	}
}

// normalizeArch maps runtime.GOARCH onto the server's cli.arch enum.
func normalizeArch(goarch string) string {
	switch goarch {
	case "amd64", "arm64", "386", "arm":
		return goarch
	default:
		return "other"
	}
}
