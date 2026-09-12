package argvio

import "time"

// defaultEndpoint is the hosted public ingest endpoint. Overridden by
// WithEndpoint for self-hosted/on-prem deployments.
//
// TODO(schema): confirm the production hostname; this is a placeholder.
const defaultEndpoint = "ingest.argvio.io:4317"

// defaultAuthHeader is the metadata/header key the api key is sent
// under.
//
// TODO(schema): confirm against the public server's actual auth
// contract; placeholder pending that being pasted into this repo.
const defaultAuthHeader = "x-argvio-api-key"

const (
	defaultExportTimeout   = 10 * time.Second
	defaultShutdownTimeout = 5 * time.Second
	defaultQueueSize       = 2048
)

type config struct {
	apiKey      string
	cliName     string
	cliVersion  string
	endpoint    string
	authHeader  string
	useHTTP     bool
	insecure    bool
	disabled    bool
	defaultTier Tier
	consent     ConsentProvider

	exportTimeout   time.Duration
	shutdownTimeout time.Duration
	queueSize       int

	extraResourceAttrs []keyValue
}

type keyValue struct {
	key, value string
}

func newConfig(apiKey, cliName, cliVersion string) *config {
	return &config{
		apiKey:          apiKey,
		cliName:         cliName,
		cliVersion:      cliVersion,
		endpoint:        defaultEndpoint,
		authHeader:      defaultAuthHeader,
		defaultTier:     TierAnonymous,
		exportTimeout:   defaultExportTimeout,
		shutdownTimeout: defaultShutdownTimeout,
		queueSize:       defaultQueueSize,
	}
}

// Option configures a Client constructed by New.
type Option func(*config)

// WithEndpoint overrides the OTLP collector endpoint (host:port for
// gRPC, or a base URL for HTTP — see WithHTTPTransport). Defaults to
// the hosted public endpoint.
func WithEndpoint(endpoint string) Option {
	return func(c *config) { c.endpoint = endpoint }
}

// WithInsecure disables transport security. Only intended for local
// development against a self-hosted collector without TLS.
func WithInsecure() Option {
	return func(c *config) { c.insecure = true }
}

// WithHTTPTransport switches the exporters from gRPC (the default) to
// OTLP/HTTP. Use when the target environment blocks outbound gRPC
// (e.g. some corporate proxies) but allows HTTPS.
func WithHTTPTransport() Option {
	return func(c *config) { c.useHTTP = true }
}

// WithAuthHeader overrides the header/metadata key the API key is sent
// under. Rarely needed; provided in case the tenant auth contract
// differs from the SDK default.
func WithAuthHeader(header string) Option {
	return func(c *config) {
		if header != "" {
			c.authHeader = header
		}
	}
}

// WithDefaultTier sets the consent tier used when no ConsentProvider is
// supplied, or as the ceiling passed to Resolve's caller when a
// provider is present but see WithConsentProvider for how the two
// interact: if a ConsentProvider is set, its resolved value is used
// (clamped to this default only if the provider errors).
func WithDefaultTier(t Tier) Option {
	return func(c *config) {
		if t.valid() {
			c.defaultTier = t
		}
	}
}

// WithConsentProvider sets the ConsentProvider used to resolve the
// session's consent tier at construction time. If unset, the tier from
// WithDefaultTier (or TierAnonymous) is used directly.
func WithConsentProvider(p ConsentProvider) Option {
	return func(c *config) { c.consent = p }
}

// WithDisabled fully disables the client: New returns a Client that
// performs zero network calls and whose Record* methods are no-ops with
// negligible overhead. Equivalent to setting the ARGVIO_DISABLED=1
// environment variable, and also engaged automatically when
// IsDoNotTrackRequested returns true.
func WithDisabled() Option {
	return func(c *config) { c.disabled = true }
}

// WithExportTimeout bounds how long a single export attempt may take.
func WithExportTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.exportTimeout = d
		}
	}
}

// WithShutdownTimeout bounds how long Shutdown/Flush may block waiting
// for in-flight data to be exported.
func WithShutdownTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.shutdownTimeout = d
		}
	}
}

// WithQueueSize bounds the number of pending records buffered before
// new records are dropped (with an in-process counter, never a blocking
// call). Applies to the trace, metric, and log batch processors.
func WithQueueSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.queueSize = n
		}
	}
}

// WithResourceAttribute attaches an additional static OTel resource
// attribute (e.g. a build channel or distribution identifier). It is
// not tier-gated and not part of the taxonomy — use sparingly, for
// attributes that describe the CLI build itself rather than any
// individual invocation.
func WithResourceAttribute(key, value string) Option {
	return func(c *config) {
		if key != "" {
			c.extraResourceAttrs = append(c.extraResourceAttrs, keyValue{key, value})
		}
	}
}
