package argvio

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName identifies this SDK to the OTel SDKs it wraps
// (tracer/meter/logger name), distinct from the CLI's own resource
// attributes.
const instrumentationName = "github.com/getargvio/argvio-go"

// Client is the entry point for emitting telemetry. It is safe for
// concurrent use, and every exported method is safe to call on a nil
// *Client or a zero-value Client (both behave as a disabled no-op) —
// vendors never need to nil-check a Client before using it.
type Client struct {
	disabled bool
	tier     Tier

	tp *sdktrace.TracerProvider
	mp *sdkmetric.MeterProvider
	lp *sdklog.LoggerProvider

	tracer trace.Tracer
	logger otellog.Logger

	latencyHist metric.Float64Histogram
	invocations metric.Int64Counter
	sessions    metric.Int64Counter
	errorEvents metric.Int64Counter

	counters *droppedCounters

	jobs      chan func()
	stopCh    chan struct{}
	wg        sync.WaitGroup
	shutdown  atomic.Bool
	sessionID atomic.Pointer[string]

	exportTimeout   time.Duration
	shutdownTimeout time.Duration
}

// New constructs a Client. apiKey, cliName, and cliVersion are always
// required, as is a collector endpoint — via WithEndpoint or the
// OTEL_EXPORTER_OTLP_ENDPOINT fallback described there. Everything else
// has a sane default and is set via Option (WithConsentProvider,
// WithDisabled, etc).
//
// New never panics and never returns a nil *Client. If construction
// encounters a problem (e.g. the exporter transport can't be built), it
// returns a Client already degraded to disabled no-op mode alongside a
// non-nil error describing why — callers may log the error but are not
// required to check it before using the returned Client safely.
func New(apiKey, cliName, cliVersion string, opts ...Option) (client *Client, err error) {
	defer func() {
		if r := recover(); r != nil {
			client, err = disabledClient(), errRecoveredPanic
		}
	}()

	cfg := newConfig(apiKey, cliName, cliVersion)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	if cfg.disabled || os.Getenv("ARGVIO_DISABLED") == "1" || IsDoNotTrackRequested() {
		return disabledClient(), nil
	}

	if cfg.endpoint == "" && !cfg.disableOTELEnvFallback {
		cfg.endpoint = os.Getenv(otelEndpointEnvVar)
	}
	if cfg.endpoint == "" {
		return disabledClient(), errNoEndpoint
	}
	if info, ok := DetectCI(); ok {
		cfg.extraResourceAttrs = append(cfg.extraResourceAttrs, info.resourceAttributes()...)
	}

	tier := resolveConsentTier(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.exportTimeout)
	defer cancel()

	exps, buildErr := buildExporters(ctx, cfg)
	if buildErr != nil {
		return disabledClient(), buildErr
	}

	res := buildResource(cfg)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(
			exps.trace,
			sdktrace.WithMaxQueueSize(cfg.queueSize),
			sdktrace.WithExportTimeout(cfg.exportTimeout),
		)),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			exps.metric,
			sdkmetric.WithTimeout(cfg.exportTimeout),
		)),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(
			exps.log,
			sdklog.WithMaxQueueSize(cfg.queueSize),
			sdklog.WithExportTimeout(cfg.exportTimeout),
		)),
	)

	meter := mp.Meter(instrumentationName)
	latencyHist, _ := meter.Float64Histogram(
		MetricCommandDuration,
		metric.WithUnit("ms"),
		metric.WithDescription("Duration of a CLI command invocation."),
	)
	invocations, _ := meter.Int64Counter(
		"cli.invocations",
		metric.WithDescription("Count of CLI command invocations."),
	)
	sessions, _ := meter.Int64Counter(
		"cli.sessions",
		metric.WithDescription("Count of CLI process sessions."),
	)
	errorEvents, _ := meter.Int64Counter(
		"cli.errors",
		metric.WithDescription("Count of recorded CLI errors."),
	)

	c := &Client{
		tier:            tier,
		tp:              tp,
		mp:              mp,
		lp:              lp,
		tracer:          tp.Tracer(instrumentationName),
		logger:          lp.Logger(instrumentationName),
		latencyHist:     latencyHist,
		invocations:     invocations,
		sessions:        sessions,
		errorEvents:     errorEvents,
		counters:        &droppedCounters{},
		jobs:            make(chan func(), cfg.queueSize),
		stopCh:          make(chan struct{}),
		exportTimeout:   cfg.exportTimeout,
		shutdownTimeout: cfg.shutdownTimeout,
	}
	c.wg.Add(1)
	go c.worker()

	return c, nil
}

func disabledClient() *Client {
	return &Client{disabled: true, tier: TierAnonymous, counters: &droppedCounters{}}
}

func resolveConsentTier(cfg *config) Tier {
	if cfg.consent == nil {
		return cfg.defaultTier
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t, err := cfg.consent.Resolve(ctx)
	if err != nil || !t.valid() {
		return cfg.defaultTier
	}
	return t
}

// enabled reports whether c is a live, non-disabled Client. Safe to
// call on nil.
func (c *Client) enabled() bool {
	return c != nil && !c.disabled && !c.shutdown.Load()
}

// dispatch queues job for asynchronous execution on the background
// worker. It never blocks: if the queue is full, job is dropped and
// counted in Stats.QueueOverflowDropped rather than applying backpressure
// to the caller's command execution.
func (c *Client) dispatch(job func()) {
	if !c.enabled() || job == nil {
		return
	}
	select {
	case c.jobs <- job:
	default:
		c.counters.queueOverflow.Add(1)
	}
}

func (c *Client) worker() {
	defer c.wg.Done()
	for {
		select {
		case job := <-c.jobs:
			c.runJob(job)
		case <-c.stopCh:
			return
		}
	}
}

func (c *Client) runJob(job func()) {
	defer recoverGuard(c.counters)
	job()
}

// Tier returns the consent tier resolved for this session.
func (c *Client) Tier() Tier {
	if c == nil {
		return TierAnonymous
	}
	return c.tier
}

// Disabled reports whether this Client is a no-op (explicitly disabled,
// DO_NOT_TRACK/CI-derived, or degraded due to a construction error).
func (c *Client) Disabled() bool {
	return c == nil || c.disabled
}

// Stats returns a snapshot of in-process counters about dropped
// records. Never sent over the network.
func (c *Client) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	return c.counters.snapshot()
}

// Flush blocks until all telemetry queued so far has been handed to the
// exporters and an export attempt has completed, or ctx is done,
// whichever comes first. Safe to call on a nil or disabled Client (a
// no-op returning nil).
func (c *Client) Flush(ctx context.Context) (err error) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err = c.drain(ctx); err != nil {
		return err
	}

	var errs []error
	if ferr := c.tp.ForceFlush(ctx); ferr != nil {
		errs = append(errs, ferr)
	}
	if ferr := c.mp.ForceFlush(ctx); ferr != nil {
		errs = append(errs, ferr)
	}
	if ferr := c.lp.ForceFlush(ctx); ferr != nil {
		errs = append(errs, ferr)
	}
	return errors.Join(errs...)
}

// drain blocks until every job dispatched before drain was called has
// finished executing on the worker goroutine, or ctx is done.
func (c *Client) drain(ctx context.Context) error {
	done := make(chan struct{})
	sentinel := func() { close(done) }

	select {
	case c.jobs <- sentinel:
	default:
		// Queue is full of real work; still attempt a blocking send so
		// drain/Flush waits for genuine backlog rather than silently
		// racing ahead, but respect ctx so we never hang past caller
		// intent.
		select {
		case c.jobs <- sentinel:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown flushes any queued telemetry (best-effort, bounded by ctx)
// and releases all background resources (worker goroutine, exporter
// connections). After Shutdown, the Client permanently behaves as a
// disabled no-op. Safe to call on a nil or disabled Client, and safe to
// call more than once.
func (c *Client) Shutdown(ctx context.Context) (err error) {
	defer recoverGuard(nil)
	if c == nil || c.disabled {
		return nil
	}
	if !c.shutdown.CompareAndSwap(false, true) {
		return nil // already shut down
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.shutdownTimeout)
		defer cancel()
	}

	_ = c.drain(ctx)
	close(c.stopCh)
	c.wg.Wait()

	var errs []error
	if serr := c.tp.Shutdown(ctx); serr != nil {
		errs = append(errs, serr)
	}
	if serr := c.mp.Shutdown(ctx); serr != nil {
		errs = append(errs, serr)
	}
	if serr := c.lp.Shutdown(ctx); serr != nil {
		errs = append(errs, serr)
	}
	return errors.Join(errs...)
}
