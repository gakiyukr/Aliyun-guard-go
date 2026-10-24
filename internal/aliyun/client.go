// Package aliyun 實作阿里雲經典 RPC API 的最小客戶端。
// 直接以 HMAC-SHA1（SignatureVersion=1.0）簽名，不引入官方 SDK：
// 本工具只用到 ECS/CDT/BSS 各少數幾個 Action，自簽名反而更輕且可控。
// 簽名規範：https://help.aliyun.com/zh/api/reference/rpc-api
package aliyun

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client 對應一組（憑證, endpoint, API 版本）組合，非並發安全複用亦可。
type Client struct {
	AK      string
	SK      string
	Domain  string
	Version string
	Scheme  string // 請求協定，空值視為 https（測試中可設為 http）
	HTTP    *http.Client
}

// New 建立客戶端；逾時比照 Python 版（連線 5s / 讀取 15s → 總逾時 20s）。
func New(ak, sk, domain, version string) *Client {
	return &Client{
		AK:      ak,
		SK:      sk,
		Domain:  domain,
		Version: version,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

// percentEncode 依阿里雲 POP 規範編碼：RFC3986 未保留字元外全部編碼，空格為 %20。
func percentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

// Call 執行一次 RPC 呼叫並回傳解析後的 JSON 物件。
// 與 Python SDK 的 do_action_with_exception 相同：業務參數置於 query string、POST 送出。
func (c *Client) Call(action string, params map[string]string) (map[string]any, error) {
	q := url.Values{}
	q.Set("Format", "JSON")
	q.Set("Version", c.Version)
	q.Set("AccessKeyId", c.AK)
	q.Set("SignatureMethod", "HMAC-SHA1")
	q.Set("SignatureVersion", "1.0")
	q.Set("SignatureNonce", fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixNano()%1_000_000))
	q.Set("Timestamp", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	q.Set("Action", action)
	for k, v := range params {
		q.Set(k, v)
	}

	// 計算 canonical query（依 key 排序後以 percentEncode 串接）
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for i, k := range keys {
		if i > 0 {
			canonical.WriteByte('&')
		}
		canonical.WriteString(percentEncode(k))
		canonical.WriteByte('=')
		canonical.WriteString(percentEncode(q.Get(k)))
	}

	// stringToSign = POST&%2F&<encoded canonical query>；金鑰為 sk + "&"。
	// 實際請求以 POST 送出（與 Python 版 set_method("POST") 一致），
	// 簽名方法前綴必須與請求方法一致，否則 SignatureDoesNotMatch。
	stringToSign := "POST&" + percentEncode("/") + "&" + percentEncode(canonical.String())
	mac := hmac.New(sha1.New, []byte(c.SK+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	scheme := c.Scheme
	if scheme == "" {
		scheme = "https"
	}
	target := scheme + "://" + c.Domain + "/?" + canonical.String() + "&Signature=" + percentEncode(signature)
	req, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("HTTP %d: 回應非 JSON（%.200s）", resp.StatusCode, string(body))
	}
	// 統一錯誤判斷：HTTP 非成功碼，或回應明確帶 Success=false（BSS 風格）
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("HTTP %d: %s: %s", resp.StatusCode, jsonStr(data, "Code"), jsonStr(data, "Message"))
	}
	if ok, exists := data["Success"]; exists {
		if b, isBool := ok.(bool); isBool && !b {
			return data, fmt.Errorf("%s: %s", jsonStr(data, "Code"), jsonStr(data, "Message"))
		}
	}
	return data, nil
}

// jsonStr 取出 JSON 物件中的字串欄位，供錯誤訊息組裝。
func jsonStr(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok && s != "" {
		return s
	}
	return "Unknown"
}

// retryableMarkers 為可重試傳輸錯誤的訊息特徵（小寫比對），對應上游
// is_retryable_aliyun_network_error() 的 markers 清單逐項收錄。
var retryableMarkers = []string{
	"connection aborted",
	"connection reset",
	"connection reset by peer",
	"connection refused",
	"connection broken",
	"remote end closed connection",
	"unexpected_eof_while_reading",
	"ssleoferror",
	"read timed out",
	"connect timeout",
	"connection timed out",
	"timed out",
	// Go 標準庫等價措辭：滿足上游以例外型別（ConnectionError/TimeoutError/
	// socket.timeout）判定的同一批傳輸故障。
	"context deadline exceeded",
	"client.timeout",
	"i/o timeout",
	"eof",
	"broken pipe",
	"no such host",
	"network is unreachable",
}

// IsRetryable 判斷是否為可重試的傳輸錯誤：以上游 markers 清單為基礎，
// 補充 Go 標準庫的等價措辭；HTTP 5xx 屬伺服器端暫時性故障，保留可重試
// （上游未涵蓋此分支，為 Go 版刻意超集）。API 業務錯誤（4xx JSON）不重試。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range retryableMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return strings.HasPrefix(msg, "http 5")
}
