package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTraceExporterPreservesContextErrorWithCustomCause 复现 SDK 的自定义截止原因：
// HTTP 可能只返回普通 cause，出口仍应保留 context 的取消类别，且不泄漏 cause 文本。
func TestTraceExporterPreservesContextErrorWithCustomCause(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OTEL_") {
			t.Setenv(name, "")
		}
	}
	for _, canceled := range []bool{false, true} {
		name := "deadline cause"
		if canceled {
			name = "cancellation cause"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			requestStarted := make(chan struct{}, 1)
			collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requestStarted <- struct{}{}
				<-release
			}))
			defer collector.Close()
			defer close(release)
			exporter, err := newTraceExporter(t.Context(), TraceConfig{
				endpoint: collector.URL + "/v1/traces", timeout: time.Second,
			}, resource.Empty(), "example-test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := exporter.Shutdown(context.Background()); err != nil {
					t.Errorf("shutdown exporter: %v", err)
				}
			})
			observation := &traceExportErrorObservation{SpanExporter: exporter.exporter}
			exporter.exporter = observation
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Errorf("shutdown provider: %v", err)
				}
			})
			_, span := provider.Tracer("synthetic-test").Start(t.Context(), "operation")
			span.End()

			cause := errors.New("synthetic-marker")
			ctx, cancel := context.WithTimeoutCause(t.Context(), 50*time.Millisecond, cause)
			defer cancel()
			want := context.DeadlineExceeded
			var cancelCause context.CancelCauseFunc
			if canceled {
				ctx, cancelCause = context.WithCancelCause(t.Context())
				defer cancelCause(nil)
				want = context.Canceled
			}
			exported := make(chan error, 1)
			go func() { exported <- exporter.ExportSpans(ctx, recorder.Ended()) }()
			if canceled {
				select {
				case <-requestStarted:
					cancelCause(cause)
				case <-exported:
					t.Fatal("export ended before the synthetic request reached its collector")
				}
			}
			err = <-exported
			// 只输出类型与布尔状态；原始 URL、头和自定义原因均不进入诊断日志。
			t.Logf("SDK error type=%T; ctx error type=%T; cause type=%T; raw matches cause=%t; ctx deadline=%t; ctx canceled=%t",
				observation.rawError, ctx.Err(), context.Cause(ctx), errors.Is(observation.rawError, cause),
				errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(ctx.Err(), context.Canceled))
			if !errors.Is(err, want) {
				t.Fatalf("ExportSpans() error = %v, want %v", err, want)
			}
			if strings.Contains(err.Error(), "synthetic-marker") {
				t.Fatal("export error exposed its custom cancellation cause")
			}
		})
	}
}

// traceExportErrorObservation 只观察真实 SDK 返回值，不替换 HTTP 行为或伪造错误。
type traceExportErrorObservation struct {
	sdktrace.SpanExporter
	rawError error
}

func (o *traceExportErrorObservation) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	o.rawError = o.SpanExporter.ExportSpans(ctx, spans)
	return o.rawError
}
