package pagination

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestCursorNormalizeLimit 保护列表默认数量和公开的 1 至 100 输入边界。
func TestCursorNormalizeLimit(t *testing.T) {
	tests := []struct {
		name  string
		value *int
		want  int
		ok    bool
	}{
		{name: "default", want: 20, ok: true},
		{name: "minimum", value: paginationIntPointer(1), want: 1, ok: true},
		{name: "maximum", value: paginationIntPointer(100), want: 100, ok: true},
		{name: "zero", value: paginationIntPointer(0)},
		{name: "negative", value: paginationIntPointer(-1)},
		{name: "above maximum", value: paginationIntPointer(101)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeLimit(test.value)
			if (err == nil) != test.ok {
				t.Fatalf("NormalizeLimit() error = %v, want ok %t", err, test.ok)
			}
			if got != test.want {
				t.Fatalf("NormalizeLimit() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestCursorRoundTrip 保护时间统一为 UTC，并保留 UUID 次级排序键。
func TestCursorRoundTrip(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	wantTime := time.Date(2026, 8, 31, 20, 15, 30, 123456789, location).UTC()
	wantID := uuid.MustParse("018f4f80-1111-7222-8333-123456789abc")

	encoded, err := EncodeCursor(Cursor{Time: wantTime.In(location), ID: wantID})
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("EncodeCursor() = %q, want raw URL-safe Base64", encoded)
	}

	got, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}
	if !got.Time.Equal(wantTime) || got.Time.Location() != time.UTC || got.ID != wantID {
		t.Fatalf("DecodeCursor() = %#v, want UTC %s and %s", got, wantTime, wantID)
	}
}

// TestCursorRejectsInvalidValues 保护客户端不能伪造缺失、损坏或扩展过的游标结构。
func TestCursorRejectsInvalidValues(t *testing.T) {
	validTime := "2026-08-31T12:15:30Z"
	validID := "018f4f80-1111-7222-8333-123456789abc"
	tests := []string{
		"",
		"not-base64",
		base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"time":"` + validTime + `","id":"` + validID + `","extra":true}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"time":"` + validTime + `","id":"` + validID + `"} trailing`)),
	}

	for _, value := range tests {
		if _, err := DecodeCursor(value); err == nil {
			t.Fatalf("DecodeCursor(%q) error = nil", value)
		}
	}

	if _, err := EncodeCursor(Cursor{}); err == nil {
		t.Fatal("EncodeCursor() accepted empty fields")
	}
}

func paginationIntPointer(value int) *int { return &value }
