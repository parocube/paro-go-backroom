// Package telemetry 提供本地追踪与受控 OTLP 导出，不拥有业务或部署规则。
package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Service 由应用声明日志关联的服务名与导出范围；二者不能取自请求或 Span 的任意属性。
type Service struct {
	Name      string
	ScopeName string
}

// New 保留原有本地追踪入口；只有显式读取配置并调用 NewWithConfig 才会启用网络导出。
func New(serviceName string) (*sdktrace.TracerProvider, error) {
	return NewWithConfig(context.Background(), Service{Name: serviceName, ScopeName: serviceName}, TraceConfig{})
}

// NewWithConfig 安装追踪提供者与 W3C 传播器；配置由 LoadTraceConfig 在启动时校验。
// 启用后异步批量发送，失败批次丢弃并输出安全错误，避免 Collector 故障阻塞业务请求。
func NewWithConfig(ctx context.Context, service Service, cfg TraceConfig) (*sdktrace.TracerProvider, error) {
	service.Name = strings.TrimSpace(service.Name)
	if service.Name == "" {
		return nil, fmt.Errorf("service name is required")
	}
	if strings.TrimSpace(service.ScopeName) == "" {
		return nil, fmt.Errorf("instrumentation scope name is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	serviceResource := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(service.Name),
	)
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(serviceResource)}
	if cfg.enabled {
		exporter, err := newTraceExporter(ctx, cfg, serviceResource, service.ScopeName)
		if err != nil {
			return nil, err
		}
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithExportTimeout(cfg.timeout)))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider, nil
}
