package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// safeTraceExporter 在唯一出口裁剪数据，并屏蔽可能包含目的地、响应体或认证信息的 SDK 错误。
type safeTraceExporter struct {
	exporter  sdktrace.SpanExporter
	transport *http.Transport
	resource  *resource.Resource
	scopeName string
}

// newTraceExporter 只创建客户端、不探测远端；独立超时与禁止重定向限定发送目的地。
func newTraceExporter(ctx context.Context, cfg TraceConfig, serviceResource *resource.Resource, scopeName string) (*safeTraceExporter, error) {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: cfg.timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   cfg.timeout,
		ResponseHeaderTimeout: cfg.timeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.timeout,
		// 不跟随跳转，也不读取代理环境变量，避免端点将追踪和认证头转送到其他地址。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	compression := otlptracehttp.NoCompression
	if cfg.gzip {
		compression = otlptracehttp.GzipCompression
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.endpoint),
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithHeaders(cfg.headers),
		otlptracehttp.WithCompression(compression),
		otlptracehttp.WithTimeout(cfg.timeout),
		// 单个批次只发一次；失败是观测降级，不以反复发送拖累登录与管理接口。
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, safeTraceError(ctx, err)
	}
	return &safeTraceExporter{exporter: exporter, transport: transport, resource: serviceResource, scopeName: scopeName}, nil
}

// ExportSpans 保留关联 ID、时间、状态和受控 HTTP 属性，其余数据不进入序列化步骤。
func (e *safeTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	safeSpans := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, span := range spans {
		safeSpans = append(safeSpans, safeTraceSpan{ReadOnlySpan: span, resource: e.resource, scopeName: e.scopeName})
	}
	return safeTraceError(ctx, e.exporter.ExportSpans(ctx, safeSpans))
}

// Shutdown 由批处理器在排队数据刷新后调用；关闭等待受调用方 context 与发送超时共同约束。
func (e *safeTraceExporter) Shutdown(ctx context.Context) error {
	defer e.transport.CloseIdleConnections()
	return safeTraceError(ctx, e.exporter.Shutdown(ctx))
}

// safeTraceError 只保留可操作的取消/超时类别，不保留含 URL、头或远端响应的原因链。
func safeTraceError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	// SDK 使用自定义超时原因时，HTTP 错误链可能只保留 cause；只取 Err 恢复安全类别。
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	// HTTP 的连接/响应头超时不一定包装 context 错误，但都实现 net.Error。
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return context.DeadlineExceeded
	}
	return errors.New("trace export failed")
}

// safeTraceSpan 提供 SDK 只读视图，明确移除事件、链接、错误描述、任意名称和 tracestate。
// http.route 必须由 API 的 chi.RoutePattern 写入，任何位置都不得以 URL.Path 作为回退。
type safeTraceSpan struct {
	sdktrace.ReadOnlySpan
	resource  *resource.Resource
	scopeName string
}

func (s safeTraceSpan) Name() string {
	if s.SpanKind() == trace.SpanKindServer || s.SpanKind() == trace.SpanKindClient {
		return "http.request"
	}
	return "operation"
}

func (s safeTraceSpan) Attributes() []attribute.KeyValue {
	var method, route string
	var status int64
	for _, field := range s.ReadOnlySpan.Attributes() {
		switch field.Key {
		case "http.request.method", "http.method":
			if field.Value.Type() == attribute.STRING {
				method = field.Value.AsString()
			}
		case "http.response.status_code", "http.status_code":
			if field.Value.Type() == attribute.INT64 {
				status = field.Value.AsInt64()
			}
		case "http.route":
			if field.Value.Type() == attribute.STRING {
				route = field.Value.AsString()
			}
		}
	}
	var fields []attribute.KeyValue
	if method != "" {
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
			http.MethodHead, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		default:
			method = "_OTHER"
		}
		fields = append(fields, attribute.String("http.request.method", method))
	}
	if status >= 100 && status <= 599 {
		fields = append(fields, attribute.Int64("http.response.status_code", status))
	}
	if strings.HasPrefix(route, "/") && len(route) <= 512 && !strings.ContainsAny(route, "?#\r\n") {
		fields = append(fields, attribute.String("http.route", route))
	}
	return fields
}

func (s safeTraceSpan) SpanContext() trace.SpanContext {
	return s.ReadOnlySpan.SpanContext().WithTraceState(trace.TraceState{})
}

func (s safeTraceSpan) Parent() trace.SpanContext {
	return s.ReadOnlySpan.Parent().WithTraceState(trace.TraceState{})
}

func (s safeTraceSpan) Resource() *resource.Resource { return s.resource }
func (s safeTraceSpan) Events() []sdktrace.Event     { return nil }
func (s safeTraceSpan) Links() []sdktrace.Link       { return nil }
func (s safeTraceSpan) Status() sdktrace.Status {
	return sdktrace.Status{Code: s.ReadOnlySpan.Status().Code}
}

func (s safeTraceSpan) InstrumentationScope() instrumentation.Scope {
	return instrumentation.Scope{Name: s.scopeName}
}

func (s safeTraceSpan) InstrumentationLibrary() instrumentation.Library { //nolint:staticcheck // 满足 SDK 兼容接口，不能透传旧字段中的任意名称。
	return s.InstrumentationScope()
}
