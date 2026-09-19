package telemetry_test

import (
	"context"
	"testing"

	"github.com/parocube/paro-go-backroom/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestNewCreatesTraceIDsAndInstallsW3CPropagation(t *testing.T) {
	// 验证当前适配器的边界：能创建本地 trace，并安装跨服务使用的 W3C 传播器。
	provider, err := telemetry.New("example-service")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	ctx, span := provider.Tracer("test").Start(context.Background(), "operation")
	spanContext := trace.SpanContextFromContext(ctx)
	span.End()
	if !spanContext.TraceID().IsValid() {
		t.Fatalf("TraceID = %s", spanContext.TraceID())
	}

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if carrier.Get("traceparent") == "" {
		t.Fatal("traceparent was not injected")
	}
}

func TestNewRejectsEmptyServiceName(t *testing.T) {
	// 服务名用于资源标识，空白值必须在初始化阶段失败。
	if _, err := telemetry.New(" "); err == nil {
		t.Fatal("New() error = nil")
	}
}

// TestNewRejectsEmptyScope 防止导出时把未定义的范围或任意 Span 名称作为默认身份。
func TestNewRejectsEmptyScope(t *testing.T) {
	if _, err := telemetry.NewWithConfig(t.Context(), telemetry.Service{Name: "example-service"}, telemetry.TraceConfig{}); err == nil {
		t.Fatal("NewWithConfig() accepted an empty instrumentation scope")
	}
}
