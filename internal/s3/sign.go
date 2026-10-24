// AWS SigV4（s3v4）簽名實作，對應 boto3 signature_version="s3v4"。
// S3 模式不對 URI 二次編碼（雙重編碼停用），路徑逐段 RFC3986 編碼保留 /。
package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// signInput 為單次請求的簽名輸入。
type signInput struct {
	method  string
	uriPath string // 已編碼的請求路徑（保留 /）
	query   map[string]string
	headers map[string]string // 已小寫鍵名的欲簽標頭
	payload []byte
	creds   Credentials
	region  string
	now     time.Time
}

// hmacSHA256 便捷封裝。
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// sha256Hex 計算資料的 SHA256 十六進位值。
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// uriEscape 實作 AWS RFC3986 編碼；keepSlash 為 true 時保留 '/'（路徑用），
// false 時 '/' 亦編碼（查詢字串鍵值用）。空格編碼為 %20（非 +）。
func uriEscape(text string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '.' || ch == '_' || ch == '~':
			b.WriteByte(ch)
		case ch == '/' && keepSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// canonicalQuery 組裝排序後的 canonical 查詢字串。
func canonicalQuery(query map[string]string) string {
	pairs := make([]string, 0, len(query))
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pairs = append(pairs, uriEscape(key, false)+"="+uriEscape(query[key], false))
	}
	return strings.Join(pairs, "&")
}

// canonicalHeaders 輸出排序後的 canonical 標頭區塊與 signed headers 字串。
func canonicalHeaders(headers map[string]string) (string, string) {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, strings.ToLower(key))
	}
	sort.Strings(keys)
	var block strings.Builder
	for _, key := range keys {
		value := strings.TrimSpace(headers[key])
		// 連續空白摺疊為單一空格（SigV4 規範）
		value = collapseSpaces(value)
		block.WriteString(key)
		block.WriteString(":")
		block.WriteString(value)
		block.WriteString("\n")
	}
	return block.String(), strings.Join(keys, ";")
}

// collapseSpaces 摺疊值中的連續空白。
func collapseSpaces(value string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range value {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// sign 產出該請求的 Authorization 標頭值。
func sign(in signInput) string {
	amzDate := in.now.UTC().Format("20060102T150405Z")
	dateStamp := in.now.UTC().Format("20060102")
	payloadHash := sha256Hex(in.payload)

	headers := map[string]string{"host": in.headers["host"], "x-amz-date": amzDate, "x-amz-content-sha256": payloadHash}
	for key, value := range in.headers {
		lower := strings.ToLower(key)
		if lower == "host" {
			continue
		}
		headers[lower] = value
	}
	headersBlock, signedHeaders := canonicalHeaders(headers)

	canonicalRequest := strings.Join([]string{
		in.method,
		in.uriPath,
		canonicalQuery(in.query),
		headersBlock,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, in.region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+in.creds.SecretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(in.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		in.creds.AccessKeyID, scope, signedHeaders, signature)
}
