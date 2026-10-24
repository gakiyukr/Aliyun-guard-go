package aliyun

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsRetryable 依上游 markers 清單驗證可重試判定：傳輸層錯誤（含大小寫、
// Go 等價措辭）應重試；API 業務錯誤與 4xx 不重試。
func TestIsRetryable(t *testing.T) {
	retryable := []string{
		"Connection Aborted",
		"read: connection reset by peer",
		"dial tcp 1.2.3.4:443: connect: connection refused",
		"connection broken: EOF",
		"remote end closed connection",
		"SSLEOFError: EOF occurred in violation of protocol",
		"Read timed out. (read timeout=10)",
		"dial tcp: connect timeout",
		"Client.Timeout exceeded while awaiting headers",
		"context deadline exceeded",
		"unexpected EOF",
		"dial tcp: lookup cdt.aliyuncs.com: no such host",
		"HTTP 502: Bad Gateway: Unknown",
		"HTTP 503: ServiceUnavailable: Unknown",
	}
	for _, msg := range retryable {
		if !IsRetryable(errors.New(msg)) {
			t.Errorf("應可重試: %q", msg)
		}
	}
	nonRetryable := []string{
		"InvalidAccessKeyId.NotFound: The AccessKey Id provided is not exist",
		"SignatureDoesNotMatch: The signature does not match",
		"MissingParameter: The parameter RegionId is required",
		"HTTP 404: NotFound: Unknown",
		"区域 cn-hangzhou 中未找到实例 i-123",
		"",
	}
	for _, msg := range nonRetryable {
		if IsRetryable(errors.New(msg)) {
			t.Errorf("不應可重試: %q", msg)
		}
	}
	if IsRetryable(nil) {
		t.Errorf("nil 不應可重試")
	}
	// wrapped error 亦依訊息判定
	if !IsRetryable(fmt.Errorf("ListCdtInternetTraffic: %w", errors.New("connection reset"))) {
		t.Errorf("wrapped 傳輸錯誤應可重試")
	}
}
