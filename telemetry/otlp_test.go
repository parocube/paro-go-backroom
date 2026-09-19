package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parocube/paro-go-backroom/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestOTLPShutdownExportsQueuedSpan 防止程序退出时直接丢弃尚未发送的追踪。
func TestOTLPShutdownExportsQueuedSpan(t *testing.T) {
	clearTraceEnvironment(t)
	received := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read export: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "60000")

	provider := configuredTraceProvider(t)
	_, span := provider.Tracer("synthetic-test").Start(t.Context(), "http.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("http.request.method", "GET"), attribute.Int("http.response.status_code", 200)),
	)
	span.End()
	select {
	case <-received:
		t.Fatal("test span was exported before shutdown began")
	default:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := provider.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case body := <-received:
		var exported collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &exported); err != nil {
			t.Fatalf("decode OTLP protobuf: %v", err)
		}
		if len(exported.ResourceSpans) != 1 || len(exported.ResourceSpans[0].ScopeSpans) != 1 ||
			len(exported.ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
			t.Fatal("collector did not receive exactly one span")
		}
		gotSpan := exported.ResourceSpans[0].ScopeSpans[0].Spans[0]
		if exported.ResourceSpans[0].ScopeSpans[0].Scope.GetName() != "example.test/tracing" {
			t.Fatal("collector scope is not the caller's static instrumentation name")
		}
		spanID := span.SpanContext().SpanID()
		if !bytes.Equal(gotSpan.SpanId, spanID[:]) || gotSpan.Name != "http.request" {
			t.Fatal("collector received a different span")
		}
		if fields := exported.ResourceSpans[0].Resource.Attributes; len(fields) != 1 ||
			fields[0].Key != "service.name" || fields[0].Value.GetStringValue() != "example-service" {
			t.Fatal("collector resource is not the configured service")
		}
	default:
		t.Fatal("collector received no queued span during shutdown")
	}
}

// TestOTLPDisabledNeverContactsCollector 保护默认不向外发送数据的边界，包括仅误配端点的场景。
func TestOTLPDisabledNeverContactsCollector(t *testing.T) {
	for _, exporter := range []string{"", "none"} {
		t.Run("exporter="+exporter, func(t *testing.T) {
			clearTraceEnvironment(t)
			var requests atomic.Int32
			collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
			}))
			defer collector.Close()
			t.Setenv("OTEL_TRACES_EXPORTER", exporter)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
			provider := configuredTraceProvider(t)
			_, span := provider.Tracer("synthetic-test").Start(t.Context(), "operation")
			span.End()
			if !span.SpanContext().TraceID().IsValid() {
				t.Fatal("disabled export lost local trace IDs")
			}
			if err := provider.Shutdown(t.Context()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			if requests.Load() != 0 {
				t.Fatal("disabled export contacted the collector")
			}
		})
	}
}

// TestOTLPConfigurationRejectsUnsafeValuesWithoutEcho 避免无效配置降级为 SDK 默认目的地或泄漏配置值。
func TestOTLPConfigurationRejectsUnsafeValuesWithoutEcho(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"exporter", map[string]string{"OTEL_TRACES_EXPORTER": "synthetic-marker"}},
		{"missing endpoint", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": ""}},
		{"relative endpoint", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "/synthetic-marker"}},
		{"endpoint user info", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://synthetic-marker@example.invalid/v1/traces"}},
		{"endpoint query", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://example.invalid/v1/traces?synthetic-marker"}},
		{"endpoint fragment", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://example.invalid/v1/traces#synthetic-marker"}},
		{"nonlocal HTTP", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://example.invalid/synthetic-marker"}},
		{"other URL scheme", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "ftp://example.invalid/synthetic-marker"}},
		{"other protocol", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "synthetic-marker"}},
		{"malformed URL", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://example.invalid/%synthetic-marker"}},
		{"invalid port", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://example.invalid:65536/v1/traces"}},
		{"missing port", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://example.invalid:/v1/traces"}},
		{"timeout unit", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_TIMEOUT": "synthetic-marker"}},
		{"timeout zero", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_TIMEOUT": "0"}},
		{"timeout too long", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_TIMEOUT": "60001"}},
		{"header pair", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "synthetic-marker"}},
		{"header newline", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "X-Test=synthetic-marker%0d%0a"}},
		{"header invalid escape", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "X-Test=%synthetic-marker"}},
		{"transport header", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "Content-Type=synthetic-marker"}},
		{"custom certificate", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE": "synthetic-marker"}},
		{"insecure override", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_INSECURE": "true"}},
		{"compression", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_COMPRESSION": "synthetic-marker"}},
		{"shadowed endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://%synthetic-marker"}},
		{"shadowed timeout", map[string]string{"OTEL_EXPORTER_OTLP_TIMEOUT": "synthetic-marker", "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT": "1000"}},
		{"shadowed headers", map[string]string{"OTEL_EXPORTER_OTLP_HEADERS": "synthetic-marker", "OTEL_EXPORTER_OTLP_TRACES_HEADERS": "X-Test=valid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearTraceEnvironment(t)
			t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://example.invalid/v1/traces")
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			_, err := telemetry.LoadTraceConfig()
			if err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			if strings.Contains(err.Error(), "synthetic-marker") || strings.Contains(err.Error(), "example.invalid") {
				t.Fatal("configuration error exposed its value")
			}
		})
	}
}

// TestOTLPEndpointAndHeaderPrecedence 通过收到的真实请求确认通用端点追加路径与 traces 专用配置优先。
func TestOTLPEndpointAndHeaderPrecedence(t *testing.T) {
	for _, specific := range []bool{false, true} {
		name := "generic"
		if specific {
			name = "traces"
		}
		t.Run(name, func(t *testing.T) {
			clearTraceEnvironment(t)
			var requests atomic.Int32
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				wantPath, wantHeader := "/base/v1/traces", "general value"
				if specific {
					wantPath, wantHeader = "/custom", "trace value"
				}
				if r.URL.Path != wantPath || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" ||
					r.Header.Get("X-Test-Collector") != wantHeader {
					t.Error("OTLP request did not honor its endpoint, protocol, or configured header")
				}
				if specific {
					if r.Header.Get("Content-Encoding") != "gzip" {
						t.Error("OTLP request ignored gzip configuration")
					}
					reader, err := gzip.NewReader(r.Body)
					if err != nil {
						t.Errorf("open gzip export: %v", err)
						return
					}
					defer func() {
						if err := reader.Close(); err != nil {
							t.Errorf("close gzip export: %v", err)
						}
					}()
					body, err := io.ReadAll(reader)
					if err != nil {
						t.Errorf("read gzip export: %v", err)
					}
					var exported collectortrace.ExportTraceServiceRequest
					if err := proto.Unmarshal(body, &exported); err != nil || len(exported.ResourceSpans) != 1 {
						t.Error("gzip export did not contain OTLP protobuf data")
					}
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer collector.Close()
			t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL+"/base/")
			t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "X-Test-Collector=general%20value")
			if specific {
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/custom")
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "X-Test-Collector=trace%20value")
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "gzip")
			}
			provider := configuredTraceProvider(t)
			endSyntheticSpan(t, provider)
			if err := provider.ForceFlush(t.Context()); err != nil {
				t.Fatalf("ForceFlush() error = %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("collector did not receive one export")
			}
		})
	}
}

// TestOTLPExportsOnlySafeSpanFields 直接检查发送字节，任何未列入允许清单的数据都不能离开进程。
func TestOTLPExportsOnlySafeSpanFields(t *testing.T) {
	clearTraceEnvironment(t)
	received := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read export: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "user.synthetic=synthetic-marker")
	provider := configuredTraceProvider(t)
	state, err := trace.ParseTraceState("synthetic=synthetic-marker")
	if err != nil {
		t.Fatal(err)
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, TraceState: state,
	})
	ctx := trace.ContextWithRemoteSpanContext(t.Context(), parent)
	_, span := provider.Tracer("synthetic-marker", trace.WithInstrumentationVersion("synthetic-marker"),
		trace.WithInstrumentationAttributes(attribute.String("synthetic", "synthetic-marker"))).Start(ctx, "synthetic-marker",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.request.method", "GET"),
			attribute.Int("http.response.status_code", 200),
			attribute.String("http.route", "/api/v1/users/{userId}"),
			attribute.String("url.full", "https://example.invalid/synthetic-marker?synthetic-marker"),
			attribute.String("url.query", "synthetic-marker"),
			attribute.String("http.request.header.authorization", "synthetic-marker"),
			attribute.String("http.request.header.cookie", "synthetic-marker"),
			attribute.String("user.email", "synthetic-marker"),
			attribute.String("client.address", "synthetic-marker"),
			attribute.String("user_agent.original", "synthetic-marker"),
		),
		trace.WithLinks(trace.Link{SpanContext: parent, Attributes: []attribute.KeyValue{attribute.String("synthetic", "synthetic-marker")}}),
	)
	span.AddEvent("synthetic-marker")
	span.RecordError(errors.New("synthetic-marker"))
	span.SetStatus(codes.Error, "synthetic-marker")
	span.End()
	if err := provider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	body := <-received
	if bytes.Contains(body, []byte("synthetic-marker")) {
		t.Fatal("OTLP payload contains disallowed span data")
	}
	var exported collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &exported); err != nil {
		t.Fatal(err)
	}
	gotSpan := exported.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if len(gotSpan.Attributes) != 3 || len(gotSpan.Events) != 0 || len(gotSpan.Links) != 0 || gotSpan.Status.Message != "" || gotSpan.TraceState != "" {
		t.Fatal("OTLP span was not restricted to safe fields")
	}
	if gotSpan.Attributes[0].Key != "http.request.method" || gotSpan.Attributes[0].Value.GetStringValue() != "GET" ||
		gotSpan.Attributes[1].Key != "http.response.status_code" || gotSpan.Attributes[1].Value.GetIntValue() != 200 ||
		gotSpan.Attributes[2].Key != "http.route" || gotSpan.Attributes[2].Value.GetStringValue() != "/api/v1/users/{userId}" {
		t.Fatal("safe HTTP fields were lost or changed")
	}
}

// TestOTLPRestrictsUntrustedHTTPValues 防止未知方法或误写的 URL 产生高基数及敏感标签。
func TestOTLPRestrictsUntrustedHTTPValues(t *testing.T) {
	clearTraceEnvironment(t)
	received := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read export: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	provider := configuredTraceProvider(t)
	_, span := provider.Tracer("synthetic-test").Start(t.Context(), "http.request", trace.WithAttributes(
		attribute.String("http.request.method", "synthetic-marker"),
		attribute.String("http.route", "/api/v1/users?synthetic-marker"),
		attribute.Int("http.response.status_code", 777),
	))
	span.End()
	if err := provider.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := <-received
	if bytes.Contains(body, []byte("synthetic-marker")) {
		t.Fatal("export retained untrusted HTTP values")
	}
	var exported collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &exported); err != nil {
		t.Fatal(err)
	}
	fields := exported.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes
	if len(fields) != 1 || fields[0].Key != "http.request.method" || fields[0].Value.GetStringValue() != "_OTHER" {
		t.Fatal("export did not normalize or reject untrusted HTTP values")
	}
}

// TestOTLPExportFailureIsSafeAndNotRetried 保证 Collector 回显内容不能进入错误，瞬时失败也不会重复发送。
func TestOTLPExportFailureIsSafeAndNotRetried(t *testing.T) {
	clearTraceEnvironment(t)
	var requests atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "synthetic-marker", http.StatusServiceUnavailable)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/synthetic-marker")
	provider := configuredTraceProvider(t)
	endSyntheticSpan(t, provider)
	err := provider.ForceFlush(t.Context())
	if err == nil {
		t.Fatal("collector failure was not reported")
	}
	if strings.Contains(err.Error(), "synthetic-marker") || strings.Contains(err.Error(), collector.URL) {
		t.Fatal("export failure exposed the endpoint or response")
	}
	if requests.Load() != 1 {
		t.Fatal("failed export was retried")
	}
}

// TestOTLPRejectsRedirect 防止 Collector 用跳转将追踪或认证头发送到另一个目的地。
func TestOTLPRejectsRedirect(t *testing.T) {
	clearTraceEnvironment(t)
	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationRequests.Add(1)
	}))
	defer destination.Close()
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	provider := configuredTraceProvider(t)
	endSyntheticSpan(t, provider)
	if err := provider.ForceFlush(t.Context()); err == nil {
		t.Fatal("redirect was accepted as a successful export")
	}
	if destinationRequests.Load() != 0 {
		t.Fatal("export followed a redirect")
	}
}

// TestOTLPTimeoutAndCancellationBoundFlush 验证慢接收端不会让导出或停机无限等待。
func TestOTLPTimeoutAndCancellationBoundFlush(t *testing.T) {
	for _, cancelFlush := range []bool{false, true} {
		name := "export timeout"
		if cancelFlush {
			name = "flush deadline"
		}
		t.Run(name, func(t *testing.T) {
			clearTraceEnvironment(t)
			release := make(chan struct{})
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer collector.Close()
			defer close(release)
			t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "50")
			if cancelFlush {
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "1000")
			}
			provider := configuredTraceProvider(t)
			endSyntheticSpan(t, provider)
			flushCtx := t.Context()
			if cancelFlush {
				var cancel context.CancelFunc
				flushCtx, cancel = context.WithTimeout(flushCtx, 10*time.Millisecond)
				defer cancel()
			}
			started := time.Now()
			err := provider.ForceFlush(flushCtx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ForceFlush() error = %v, want deadline exceeded", err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("flush did not respect its deadline")
			}
		})
	}
}

// TestOTLPShutdownDeadline 保护关停期限；Collector 未响应时调用方仍能按时退出。
func TestOTLPShutdownDeadline(t *testing.T) {
	clearTraceEnvironment(t)
	release := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer collector.Close()
	defer close(release)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "1000")
	provider := configuredTraceProvider(t)
	endSyntheticSpan(t, provider)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := provider.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("shutdown exceeded its deadline")
	}
}

// TestOTLPRejectsUntrustedTLS 验证 TLS 不因接收端证书无效而降级或跳过验证。
func TestOTLPRejectsUntrustedTLS(t *testing.T) {
	clearTraceEnvironment(t)
	var requests atomic.Int32
	collector := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	collector.Config.ErrorLog = log.New(io.Discard, "", 0)
	collector.StartTLS()
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	provider := configuredTraceProvider(t)
	endSyntheticSpan(t, provider)
	err := provider.ForceFlush(t.Context())
	if err == nil || strings.Contains(err.Error(), collector.URL) {
		t.Fatal("invalid TLS was accepted or its destination was exposed")
	}
	if requests.Load() != 0 {
		t.Fatal("untrusted TLS collector received an HTTP export")
	}
}

// TestOTLPConstructionHonorsCanceledContext 防止取消后的启动继续安装全局追踪器。
func TestOTLPConstructionHonorsCanceledContext(t *testing.T) {
	clearTraceEnvironment(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:4318/v1/traces")
	cfg, err := telemetry.LoadTraceConfig()
	if err != nil {
		t.Fatal(err)
	}
	previousProvider := otel.GetTracerProvider()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := telemetry.NewWithConfig(ctx, telemetry.Service{Name: "example-service", ScopeName: "example.test/tracing"}, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewWithConfig() error = %v, want canceled", err)
	}
	if otel.GetTracerProvider() != previousProvider {
		t.Fatal("canceled construction replaced the global provider")
	}
}

// clearTraceEnvironment 防止开发机现有 OTEL 配置让测试连接真实 Collector；值不会被输出。
func clearTraceEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OTEL_") {
			t.Setenv(name, "")
		}
	}
}

func configuredTraceProvider(t *testing.T) *sdktrace.TracerProvider {
	t.Helper()
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	cfg, err := telemetry.LoadTraceConfig()
	if err != nil {
		t.Fatalf("load trace configuration: %v", err)
	}
	provider, err := telemetry.NewWithConfig(t.Context(), telemetry.Service{Name: "example-service", ScopeName: "example.test/tracing"}, cfg)
	if err != nil {
		t.Fatalf("construct tracing: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return provider
}

func endSyntheticSpan(t *testing.T, provider *sdktrace.TracerProvider) {
	t.Helper()
	_, span := provider.Tracer("synthetic-test").Start(t.Context(), "operation")
	span.End()
}
