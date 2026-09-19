// Package logging 创建带调用方服务标识的结构化日志器，不读取业务配置。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config 定义日志级别、输出格式和输出目标；无效级别或格式会使 New 返回错误。
type Config struct {
	// Level 使用 slog 支持的日志级别；无效值必须在启动时拒绝，避免运行期间悄悄丢失日志。
	Level string
	// Format 只允许 json 或 text，确保日志采集端能按约定解析输出。
	Format string
	// Writer 允许测试或调用方接收日志；未提供时才写入标准输出。
	Writer io.Writer
}

// New 根据配置创建带固定服务名的日志器；配置不合法时返回错误而不降级为默认格式。
func New(serviceName string, config Config) (*slog.Logger, error) {
	if strings.TrimSpace(serviceName) == "" {
		return nil, fmt.Errorf("log service name is required")
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(config.Level)); err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}

	writer := config.Writer
	if writer == nil {
		writer = os.Stdout
	}
	options := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	// 日志格式是对外可见的运维约定，只接受已明确支持的两种格式。
	switch config.Format {
	case "json":
		handler = slog.NewJSONHandler(writer, options)
	case "text":
		handler = slog.NewTextHandler(writer, options)
	default:
		return nil, fmt.Errorf("log format is invalid")
	}

	return slog.New(handler).With("service", serviceName), nil
}
