package telemetry

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"golang.org/x/net/http/httpguts"
)

const defaultExportTimeout = 10 * time.Second

// TraceConfig 保存已校验的导出设置；零值保持本地追踪，不产生网络请求。
// 字段不对外开放，避免调用方绕过端点和凭据的校验；禁止记录整个配置。
type TraceConfig struct {
	enabled  bool
	endpoint string
	headers  map[string]string
	timeout  time.Duration
	gzip     bool
}

// LoadTraceConfig 在启动时读取标准 OTEL 变量。只有显式选择 otlp 才允许导出；
// 出错只返回变量名和规则，绝不拼入端点、认证头或底层解析错误。
func LoadTraceConfig() (TraceConfig, error) {
	switch strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")) {
	case "", "none":
		return TraceConfig{}, nil
	case "otlp":
	default:
		return TraceConfig{}, errors.New("OTEL_TRACES_EXPORTER must be none or otlp")
	}
	if err := validateBatchConfig(); err != nil {
		return TraceConfig{}, err
	}

	// 首批只支持系统信任库 TLS；不悄悄忽略证书配置或允许关闭服务端证书校验。
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_CERTIFICATE", "OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", "OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_CLIENT_KEY", "OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY",
		"OTEL_EXPORTER_OTLP_INSECURE", "OTEL_EXPORTER_OTLP_TRACES_INSECURE",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return TraceConfig{}, fmt.Errorf("%s is unsupported; use an HTTPS endpoint with system trust or loopback HTTP", name)
		}
	}

	protocol := traceEnv("PROTOCOL")
	if protocol != "" && protocol != "http/protobuf" {
		return TraceConfig{}, errors.New("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL must be http/protobuf")
	}
	compression := traceEnv("COMPRESSION")
	if compression != "" && compression != "none" && compression != "gzip" {
		return TraceConfig{}, errors.New("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION must be none or gzip")
	}

	cfg := TraceConfig{enabled: true, timeout: defaultExportTimeout, gzip: compression == "gzip"}
	// SDK 会先读取通用变量再读取 traces 变量。两组都校验，防止被覆盖的错误值被 SDK 原样写入日志。
	for _, prefix := range []string{"OTEL_EXPORTER_OTLP_", "OTEL_EXPORTER_OTLP_TRACES_"} {
		if raw := strings.TrimSpace(os.Getenv(prefix + "ENDPOINT")); raw != "" {
			endpoint, err := parseTraceEndpoint(raw)
			if err != nil {
				return TraceConfig{}, fmt.Errorf("%sENDPOINT: %w", prefix, err)
			}
			if prefix == "OTEL_EXPORTER_OTLP_" {
				endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/traces"
			}
			cfg.endpoint = endpoint.String()
		}
		if raw := strings.TrimSpace(os.Getenv(prefix + "TIMEOUT")); raw != "" {
			milliseconds, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || milliseconds < 1 || milliseconds > 60000 {
				return TraceConfig{}, fmt.Errorf("%sTIMEOUT must be an integer from 1 to 60000 milliseconds", prefix)
			}
			cfg.timeout = time.Duration(milliseconds) * time.Millisecond
		}
		if raw := strings.TrimSpace(os.Getenv(prefix + "HEADERS")); raw != "" {
			headers, err := parseTraceHeaders(raw)
			if err != nil {
				return TraceConfig{}, fmt.Errorf("%sHEADERS: %w", prefix, err)
			}
			cfg.headers = headers
		}
	}
	if cfg.endpoint == "" {
		return TraceConfig{}, errors.New("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or OTEL_EXPORTER_OTLP_ENDPOINT is required when exporting traces")
	}
	return cfg, nil
}

// validateBatchConfig 在 SDK 读取变量前拒绝危险容量与时间，避免负数 make、零延迟空转或过量分配。
// SDK 不会去掉空白，因此这里也按原文解析；包括会被导出超时覆盖的 BSP_EXPORT_TIMEOUT，
// 防止 SDK 先记录原始错误值、随后才应用显式 option。
func validateBatchConfig() error {
	queueSize := sdktrace.DefaultMaxQueueSize
	explicitBatchSize := 0
	for _, rule := range []struct {
		name  string
		limit int
	}{
		{"OTEL_BSP_MAX_QUEUE_SIZE", 65536},
		{"OTEL_BSP_MAX_EXPORT_BATCH_SIZE", 4096},
		{"OTEL_BSP_SCHEDULE_DELAY", 60000},
		{"OTEL_BSP_EXPORT_TIMEOUT", 60000},
	} {
		raw := os.Getenv(rule.name)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > rule.limit {
			return fmt.Errorf("%s must be an integer from 1 to %d", rule.name, rule.limit)
		}
		switch rule.name {
		case "OTEL_BSP_MAX_QUEUE_SIZE":
			queueSize = value
		case "OTEL_BSP_MAX_EXPORT_BATCH_SIZE":
			explicitBatchSize = value
		}
	}
	if explicitBatchSize > queueSize {
		return errors.New("OTEL_BSP_MAX_EXPORT_BATCH_SIZE must not exceed OTEL_BSP_MAX_QUEUE_SIZE")
	}
	return nil
}

// traceEnv 遵守 traces 专用值优先、空值视为未设置的标准配置规则。
func traceEnv(suffix string) string {
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_" + suffix)); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_" + suffix))
}

// parseTraceEndpoint 只接受明确目的地；认证必须放请求头，不能藏在 URL 中。
func parseTraceEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || endpoint.Opaque != "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("must be an absolute HTTP(S) URL without user info, query, or fragment")
	}
	if endpoint.Scheme != "https" && endpoint.Scheme != "http" {
		return nil, errors.New("must use HTTPS or loopback HTTP")
	}
	if endpoint.Scheme == "http" {
		address := net.ParseIP(endpoint.Hostname())
		if endpoint.Hostname() != "localhost" && (address == nil || !address.IsLoopback()) {
			return nil, errors.New("plain HTTP is only allowed for loopback collectors")
		}
	}
	if port := endpoint.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
	} else if strings.HasSuffix(endpoint.Host, ":") {
		return nil, errors.New("port must not be empty")
	}
	if endpoint.Path == "" {
		endpoint.Path = "/"
	}
	return endpoint, nil
}

// parseTraceHeaders 支持标准的逗号分隔与百分号编码，不允许注入换行或覆盖传输头。
func parseTraceHeaders(raw string) (map[string]string, error) {
	headers := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		name, value, found := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if !found || !httpguts.ValidHeaderFieldName(name) {
			return nil, errors.New("must contain valid name=value pairs")
		}
		value, err := url.PathUnescape(value)
		if err != nil || !httpguts.ValidHeaderFieldValue(value) {
			return nil, errors.New("contains an invalid encoded header value")
		}
		switch strings.ToLower(name) {
		case "host", "content-type", "content-length", "content-encoding", "connection", "transfer-encoding", "trailer":
			return nil, errors.New("must not override HTTP transport headers")
		}
		headers[name] = strings.TrimSpace(value)
	}
	return headers, nil
}
