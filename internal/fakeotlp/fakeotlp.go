// Package fakeotlp is an in-memory OTLP/gRPC receiver used only by this
// module's own tests, so the test suite never needs a running `public`
// server. It is not part of the public API (hence internal/) and makes
// no compatibility promises.
package fakeotlp

import (
	"context"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	cotrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Server is an in-memory OTLP/gRPC receiver implementing the trace,
// metrics, and logs collector services. Every request it receives is
// recorded and can be inspected via Traces, Metrics, and Logs.
type Server struct {
	cotrace.UnimplementedTraceServiceServer

	mu      sync.Mutex
	traces  []*tracepb.ResourceSpans
	metrics []*metricspb.ResourceMetrics
	logs    []*logspb.ResourceLogs
	headers []metadata.MD

	grpcServer *grpc.Server
	listener   net.Listener
}

// Start starts the fake receiver on an OS-assigned local port and
// returns it. Call Close when done (typically via t.Cleanup).
func Start() (*Server, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	s := &Server{listener: lis}
	s.grpcServer = grpc.NewServer()
	cotrace.RegisterTraceServiceServer(s.grpcServer, s)
	commetrics.RegisterMetricsServiceServer(s.grpcServer, metricsServer{Server: s})
	collogs.RegisterLogsServiceServer(s.grpcServer, logsServer{Server: s})

	go func() {
		_ = s.grpcServer.Serve(lis)
	}()

	return s, nil
}

// Addr returns the "host:port" the fake receiver is listening on,
// suitable for argvio.WithEndpoint plus argvio.WithInsecure.
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// DialOption returns the grpc.DialOption test callers would otherwise
// need to construct themselves for an insecure local connection.
func (s *Server) DialOption() grpc.DialOption {
	return grpc.WithTransportCredentials(insecure.NewCredentials())
}

// Close stops the receiver and releases its listener.
func (s *Server) Close() {
	s.grpcServer.Stop()
}

// Export implements the OTLP TraceService.
func (s *Server) Export(ctx context.Context, req *cotrace.ExportTraceServiceRequest) (*cotrace.ExportTraceServiceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = append(s.traces, req.GetResourceSpans()...)
	s.recordHeaders(ctx)
	return &cotrace.ExportTraceServiceResponse{}, nil
}

// metricsServer and logsServer adapt *Server to the MetricsService and
// LogsService interfaces: Go doesn't allow a single type to declare two
// methods both named Export with different signatures, so each signal
// gets a thin wrapper type that embeds *Server for shared state plus
// the interface's required Unimplemented*Server for forward
// compatibility.
type metricsServer struct {
	*Server
	commetrics.UnimplementedMetricsServiceServer
}

func (m metricsServer) Export(ctx context.Context, req *commetrics.ExportMetricsServiceRequest) (*commetrics.ExportMetricsServiceResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = append(m.metrics, req.GetResourceMetrics()...)
	m.recordHeaders(ctx)
	return &commetrics.ExportMetricsServiceResponse{}, nil
}

type logsServer struct {
	*Server
	collogs.UnimplementedLogsServiceServer
}

func (l logsServer) Export(ctx context.Context, req *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, req.GetResourceLogs()...)
	l.recordHeaders(ctx)
	return &collogs.ExportLogsServiceResponse{}, nil
}

func (s *Server) recordHeaders(ctx context.Context) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.headers = append(s.headers, md)
	}
}

// Traces returns every ResourceSpans received so far.
func (s *Server) Traces() []*tracepb.ResourceSpans {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*tracepb.ResourceSpans, len(s.traces))
	copy(out, s.traces)
	return out
}

// Metrics returns every ResourceMetrics received so far.
func (s *Server) Metrics() []*metricspb.ResourceMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*metricspb.ResourceMetrics, len(s.metrics))
	copy(out, s.metrics)
	return out
}

// Logs returns every ResourceLogs received so far.
func (s *Server) Logs() []*logspb.ResourceLogs {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*logspb.ResourceLogs, len(s.logs))
	copy(out, s.logs)
	return out
}

// Headers returns the gRPC metadata captured from every request
// received so far, across all three signal types — useful for
// asserting the API key header was sent correctly.
func (s *Server) Headers() []metadata.MD {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metadata.MD, len(s.headers))
	copy(out, s.headers)
	return out
}

// Reset clears all recorded requests without stopping the server.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = nil
	s.metrics = nil
	s.logs = nil
	s.headers = nil
}
