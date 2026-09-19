package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/parocube/paro-go-backroom/logging"
)

func TestNewJSONLoggerIncludesService(t *testing.T) {
	// 使用独立缓冲区，避免测试依赖或污染进程的标准输出。
	var output bytes.Buffer
	logger, err := logging.New("example-service", logging.Config{
		Level:  "info",
		Format: "json",
		Writer: &output,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	logger.InfoContext(context.Background(), "started", "operation", "test")

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if entry["service"] != "example-service" {
		t.Fatalf("service = %v", entry["service"])
	}
	if entry["operation"] != "test" {
		t.Fatalf("operation = %v", entry["operation"])
	}
}

func TestNewRejectsUnknownLevelAndFormat(t *testing.T) {
	// 启动阶段必须同时拒绝未知日志级别和格式，不能默默采用默认配置。
	tests := []logging.Config{
		{Level: "verbose", Format: "json"},
		{Level: "info", Format: "xml"},
	}
	for _, cfg := range tests {
		if _, err := logging.New("example-service", cfg); err == nil {
			t.Fatalf("New(%+v) error = nil", cfg)
		}
	}
}

// TestNewRejectsMissingService 防止公共日志器误用来源不明或硬编码的服务标识。
func TestNewRejectsMissingService(t *testing.T) {
	if _, err := logging.New(" ", logging.Config{Level: "info", Format: "json"}); err == nil {
		t.Fatal("New() accepted a missing service name")
	}
}
