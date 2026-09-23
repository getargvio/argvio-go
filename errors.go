package argvio

import "errors"

var (
	errNoProviderResolved = errors.New("argvio: no consent provider resolved a tier")
	errRecoveredPanic     = errors.New("argvio: recovered from internal panic")
	errNoEndpoint         = errors.New("argvio: no endpoint configured; pass argvio.WithEndpoint or set OTEL_EXPORTER_OTLP_ENDPOINT")
)
