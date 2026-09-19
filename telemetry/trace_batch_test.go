package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/parocube/paro-go-backroom/telemetry"
	"go.opentelemetry.io/otel"
)

// TestOTLPBatchConfigurationFailsBeforeSDK 防止负容量导致 panic、零延迟空转，
// 或 SDK 把无法解析的原始配置写入日志。必须先返回安全配置错误，不能依靠 SDK 回退。
func TestOTLPBatchConfigurationFailsBeforeSDK(t *testing.T) {
	variables := []struct {
		name     string
		tooLarge string
	}{
		{"OTEL_BSP_MAX_QUEUE_SIZE", "65537"},
		{"OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "4097"},
		{"OTEL_BSP_SCHEDULE_DELAY", "60001"},
		{"OTEL_BSP_EXPORT_TIMEOUT", "60001"},
	}
	for _, variable := range variables {
		for _, invalid := range []struct{ name, value string }{
			{"negative", "-1"}, {"zero", "0"}, {"too large", variable.tooLarge},
			{"malformed", "synthetic-marker"}, {"whitespace", " 1 "}, {"overflow", "9223372036854775808"},
		} {
			t.Run(variable.name+"/"+invalid.name, func(t *testing.T) {
				clearTraceEnvironment(t)
				t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://example.invalid/v1/traces")
				t.Setenv(variable.name, invalid.value)
				previousProvider := otel.GetTracerProvider()
				previousPropagator := otel.GetTextMapPropagator()
				providerCreated := false
				defer func() {
					if providerCreated {
						otel.SetTracerProvider(previousProvider)
						otel.SetTextMapPropagator(previousPropagator)
					}
					if recover() != nil {
						t.Error("invalid batch configuration reached the SDK and caused a construction panic")
					}
				}()
				cfg, err := telemetry.LoadTraceConfig()
				if err == nil {
					provider, constructionErr := telemetry.NewWithConfig(t.Context(), telemetry.Service{Name: "example-service", ScopeName: "example.test/tracing"}, cfg)
					if constructionErr == nil {
						providerCreated = true
						shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						if err := provider.Shutdown(shutdownCtx); err != nil {
							t.Errorf("shutdown invalid construction: %v", err)
						}
					}
					t.Fatal("invalid batch configuration was accepted before SDK construction")
				}
				if !strings.Contains(err.Error(), variable.name) || strings.Contains(err.Error(), "synthetic-marker") {
					t.Fatal("configuration error did not safely identify the batch variable")
				}
			})
		}
	}
}

// TestOTLPBatchSizeCannotExceedQueue 防止显式配置被 SDK 悄悄改小，实际容量应由启动校验说明。
func TestOTLPBatchSizeCannotExceedQueue(t *testing.T) {
	clearTraceEnvironment(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://example.invalid/v1/traces")
	t.Setenv("OTEL_BSP_MAX_QUEUE_SIZE", "1")
	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "2")
	if _, err := telemetry.LoadTraceConfig(); err == nil {
		t.Fatal("batch larger than the queue was accepted")
	}
}

// TestOTLPValidBatchSizeSendsWithoutFlush 验证合法批次配置仍会驱动真实导出，而不是全部被忽略。
func TestOTLPValidBatchSizeSendsWithoutFlush(t *testing.T) {
	clearTraceEnvironment(t)
	received := make(chan struct{}, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_BSP_MAX_QUEUE_SIZE", "2")
	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "60000")
	t.Setenv("OTEL_BSP_EXPORT_TIMEOUT", "1000")
	provider := configuredTraceProvider(t)
	endSyntheticSpan(t, provider)
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("configured batch size did not trigger an export")
	}
}
