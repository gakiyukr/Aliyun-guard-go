// 零依賴 S3 客戶端，涵蓋 s3_backup.py 用到的五個操作：ListObjectsV2 /
// PutObject / DeleteObject / HeadObject / GetObject。上傳恆為單一 PUT
// （上游 multipart 閾值刻意高於備份檔上限）。重試語意對應 boto3
// retries={"max_attempts":3,"mode":"standard"}：5xx 與傳輸錯誤重試 2 次。
package s3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ConnectTimeout / ReadTimeout 與上游 client 參數一致。
const (
	connectTimeout = 10 * time.Second
	readTimeout    = 60 * time.Second
	maxAttempts    = 3
	MaxRemoteItems = 5000
)

// Credentials 為 S3 存取憑證；AccessKeyID 為空代表使用環境/IAM Role 鏈。
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Config 為客戶端組態，欄位對應上游 s3_backup 設定。
type Config struct {
	Bucket          string
	Region          string
	Endpoint        string // 自訂 endpoint（MinIO 等），空則 AWS 官方
	AddressingStyle string // auto / path / virtual
	Creds           Credentials
}

// Client 為 S3 客戶端。
type Client struct {
	cfg    Config
	scheme string
	host   string // 不含 bucket 前綴的 host
	http   *http.Client
}

// Object 為 ListObjectsV2 回傳的物件摘要。
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

// ListPage 為分頁結果。
type ListPage struct {
	Contents  []Object
	Truncated bool
	NextToken string
}

// New 建立客戶端（解析 endpoint 與位址風格）。
func New(cfg Config) (*Client, error) {
	host := ""
	scheme := "https"
	if cfg.Endpoint != "" {
		parsed, err := url.Parse(cfg.Endpoint)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("S3 Endpoint 必须是无账号密码的 HTTP/HTTPS 地址")
		}
		scheme = parsed.Scheme
		host = parsed.Host
	} else {
		host = "s3." + cfg.Region + ".amazonaws.com"
	}
	return &Client{cfg: cfg, scheme: scheme, host: host, http: &http.Client{Timeout: readTimeout}}, nil
}

// requestURL 依位址風格組出請求 host 與 path。
// 對齊 boto3 auto：自訂 endpoint 一律 path-style；AWS 官方 endpoint 走 virtual-host。
func (c *Client) requestURL(key string) (host, path string) {
	bucket := c.cfg.Bucket
	switch {
	case c.cfg.Endpoint != "" && c.cfg.AddressingStyle == "virtual":
		return bucket + "." + c.host, "/" + uriEscape(key, true)
	case c.cfg.Endpoint == "" && c.cfg.AddressingStyle != "path":
		// AWS 官方 + auto/virtual → virtual-host
		return bucket + "." + c.host, "/" + uriEscape(key, true)
	default:
		return c.host, "/" + uriEscape(bucket, true) + "/" + uriEscape(key, true)
	}
}

// do 執行一次已簽名請求（含重試），回傳回應（body 未讀）。
func (c *Client) do(ctx context.Context, method, key string, query map[string]string, headers map[string]string, payload []byte) (*http.Response, error) {
	creds := c.cfg.Creds
	if creds.AccessKeyID == "" {
		resolved, err := ResolveCredentials(ctx)
		if err != nil {
			return nil, err
		}
		creds = resolved
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		host, path := c.requestURL(key)
		now := time.Now()
		payloadHash := sha256Hex(payload)
		reqHeaders := map[string]string{
			"host":                 host,
			"x-amz-date":           now.UTC().Format("20060102T150405Z"),
			"x-amz-content-sha256": payloadHash,
		}
		for k, v := range headers {
			reqHeaders[strings.ToLower(k)] = v
		}
		auth := sign(signInput{
			method:  method,
			uriPath: path,
			query:   query,
			headers: reqHeaders,
			payload: payload,
			creds:   creds,
			region:  c.cfg.Region,
			now:     now,
		})
		rawQuery := ""
		if len(query) > 0 {
			// 查詢字串以 canonical 順序輸出，與簽名一致
			rawQuery = canonicalQuery(query)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.scheme+"://"+host+path+"?"+rawQuery, strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		for k, v := range reqHeaders {
			if k == "host" {
				continue
			}
			req.Header.Set(k, v)
		}
		req.Header.Set("Authorization", auth)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// s3Error 解析 S3 XML 錯誤回應的 Code 與 Message。
func s3Error(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var parsed struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &parsed); err == nil && parsed.Code != "" {
		return fmt.Errorf("%s: %s", parsed.Code, parsed.Message)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// ListObjectsV2 列出前綴下物件（單頁）；token 為分頁 token（可空）。
func (c *Client) ListObjectsV2(ctx context.Context, prefix string, maxKeys int, token string) (*ListPage, error) {
	query := map[string]string{
		"list-type": "2",
		"prefix":    prefix,
		"max-keys":  fmt.Sprintf("%d", maxKeys),
	}
	if token != "" {
		query["continuation-token"] = token
	}
	resp, err := c.do(ctx, "GET", "/", query, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, s3Error(resp)
	}
	defer resp.Body.Close()
	var wrapper struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Contents []struct {
			Key          string    `xml:"Key"`
			Size         int64     `xml:"Size"`
			LastModified time.Time `xml:"LastModified"`
			ETag         string    `xml:"ETag"`
		} `xml:"Contents"`
		IsTruncated bool   `xml:"IsTruncated"`
		NextToken   string `xml:"NextContinuationToken"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := xml.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("读取 S3 备份列表失败: %v", err)
	}
	page := &ListPage{Truncated: wrapper.IsTruncated, NextToken: wrapper.NextToken}
	for _, item := range wrapper.Contents {
		page.Contents = append(page.Contents, Object{Key: item.Key, Size: item.Size, LastModified: item.LastModified, ETag: item.ETag})
	}
	return page, nil
}

// PutObject 單件上傳（Content-Type、x-amz-meta-format、SSE 依參數）。
func (c *Client) PutObject(ctx context.Context, key string, body []byte, meta map[string]string, sse, kmsKeyID string) error {
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range meta {
		headers["x-amz-meta-"+k] = v
	}
	if sse != "" {
		headers["x-amz-server-side-encryption"] = sse
	}
	if kmsKeyID != "" {
		headers["x-amz-server-side-encryption-aws-kms-key-id"] = kmsKeyID
	}
	resp, err := c.do(ctx, "PUT", key, nil, headers, body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return s3Error(resp)
	}
	resp.Body.Close()
	return nil
}

// DeleteObject 刪除單一物件。
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "DELETE", key, nil, nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		return nil
	}
	return s3Error(resp)
}

// HeadObject 回傳物件大小（位元組）；不存在時錯誤含 "404"。
func (c *Client) HeadObject(ctx context.Context, key string) (int64, error) {
	resp, err := c.do(ctx, "HEAD", key, nil, nil, nil)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()
	return resp.ContentLength, nil
}

// GetObject 下載物件內容（上限 88MiB，超限即報錯）。
func (c *Client) GetObject(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, "GET", key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, s3Error(resp)
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, MaxBackupFileBytesForRead))
}

// MaxBackupFileBytesForRead 與備份密文檔上限一致（s3backup 層再驗一次）。
const MaxBackupFileBytesForRead = 88*1024*1024 + 1

// ResolveCredentials 以環境變數 → IMDSv2 順序解析憑證（對應 boto3 預設鏈）。
func ResolveCredentials(ctx context.Context) (Credentials, error) {
	if ak := os.Getenv("AWS_ACCESS_KEY_ID"); ak != "" {
		return Credentials{
			AccessKeyID:     ak,
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		}, nil
	}
	client := &http.Client{Timeout: connectTimeout}
	imds := "http://169.254.169.254"
	tokenReq, err := http.NewRequestWithContext(ctx, "PUT", imds+"/latest/api/token", nil)
	if err != nil {
		return Credentials{}, err
	}
	tokenReq.Header.Set("x-aws-ec2-metadata-token-ttl-seconds", "21600")
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return Credentials{}, fmt.Errorf("無法取得 S3 憑證（環境變數與 IMDS 皆失敗）: %v", err)
	}
	token, _ := io.ReadAll(io.LimitReader(tokenResp.Body, 4096))
	tokenResp.Body.Close()

	roleReq, _ := http.NewRequestWithContext(ctx, "GET", imds+"/latest/meta-data/iam/security-credentials/", nil)
	roleReq.Header.Set("x-aws-ec2-metadata-token", strings.TrimSpace(string(token)))
	roleResp, err := client.Do(roleReq)
	if err != nil {
		return Credentials{}, fmt.Errorf("無法取得 S3 憑證: %v", err)
	}
	roleName, _ := io.ReadAll(io.LimitReader(roleResp.Body, 4096))
	roleResp.Body.Close()

	credReq, _ := http.NewRequestWithContext(ctx, "GET",
		imds+"/latest/meta-data/iam/security-credentials/"+strings.TrimSpace(string(roleName)), nil)
	credReq.Header.Set("x-aws-ec2-metadata-token", strings.TrimSpace(string(token)))
	credResp, err := client.Do(credReq)
	if err != nil {
		return Credentials{}, fmt.Errorf("無法取得 S3 憑證: %v", err)
	}
	defer credResp.Body.Close()
	var payload struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"Token"`
	}
	if err := json.NewDecoder(credResp.Body).Decode(&payload); err != nil {
		return Credentials{}, fmt.Errorf("IMDS 憑證解析失敗: %v", err)
	}
	return Credentials(payload), nil
}
