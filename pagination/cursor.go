// Package pagination 提供时间线列表共用的数量边界和不透明游标。
package pagination

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultLimit 是未指定单页数量时的统一默认值。
	DefaultLimit = 20
	// MaxLimit 限定单次列表请求的最大数量。
	MaxLimit        = 100
	maxCursorLength = 1024
)

// Cursor 保存按时间倒序分页所需的时间和 UUID 次级排序键。
// Time 编码前统一为 UTC；两个字段缺失或无效时编码和解码都会失败。
type Cursor struct {
	Time time.Time `json:"time"`
	ID   uuid.UUID `json:"id"`
}

// NormalizeLimit 应用默认数量 20，并把调用方输入限制在 1 至 100。
func NormalizeLimit(value *int) (int, error) {
	if value == nil {
		return DefaultLimit, nil
	}
	if *value < 1 || *value > MaxLimit {
		return 0, errors.New("pagination limit is invalid")
	}
	return *value, nil
}

// EncodeCursor 把已验证的时间和 UUID 编码为不带填充的 URL-safe Base64。
func EncodeCursor(value Cursor) (string, error) {
	if value.Time.IsZero() || value.ID == uuid.Nil {
		return "", errors.New("pagination cursor fields are invalid")
	}
	value.Time = value.Time.UTC()
	payload, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("encode pagination cursor")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > maxCursorLength {
		return "", errors.New("pagination cursor is too long")
	}
	return encoded, nil
}

// DecodeCursor 解码并严格校验客户端游标；损坏、缺失和附加 JSON 字段都返回错误。
func DecodeCursor(value string) (Cursor, error) {
	if value == "" || len(value) > maxCursorLength {
		return Cursor{}, errors.New("pagination cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, errors.New("pagination cursor is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor Cursor
	if err := decoder.Decode(&cursor); err != nil {
		return Cursor{}, errors.New("pagination cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Cursor{}, errors.New("pagination cursor is invalid")
	}
	if cursor.Time.IsZero() || cursor.ID == uuid.Nil {
		return Cursor{}, errors.New("pagination cursor is invalid")
	}
	cursor.Time = cursor.Time.UTC()
	return cursor, nil
}
