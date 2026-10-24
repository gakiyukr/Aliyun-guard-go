// 設定正規化與驗證，對應 s3_backup.py 的 normalized_config / validate_config，
// 驗證順序與錯誤訊息逐字對齊。
package s3backup

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"aliyun-guard-go/internal/config"
)

// Config 為 S3 備份設定（欄位對應上游 DEFAULT_CONFIG）。
type Config struct {
	Enabled              bool
	IncludeState         bool
	IncludeLogs          bool
	Bucket               string
	Region               string
	EndpointURL          string
	AddressingStyle      string
	Prefix               string
	AccessKeyID          string
	SecretAccessKey      string
	SessionToken         string
	BackupPassword       string
	Schedule             string
	Time                 string
	Weekday              int
	Retention            int
	NotificationMode     string
	ServerSideEncryption string
	KMSKeyID             string
}

// timeRe 對應上游 time 欄位的 HH:MM 正則。
var timeRe = regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d$`)

// NormalizedConfig 從 config.json 的 map 區段組出正規化設定，對應
// normalized_config：字串去空白、endpoint 去尾斜線、prefix 去首尾斜線。
func NormalizedConfig(value map[string]any) (*Config, error) {
	cfg := &Config{
		Enabled:              false,
		IncludeState:         true,
		IncludeLogs:          false,
		Bucket:               "",
		Region:               "us-east-1",
		EndpointURL:          "",
		AddressingStyle:      "auto",
		Prefix:               "aliyun-guard",
		Schedule:             "daily",
		Time:                 "03:00",
		Weekday:              0,
		Retention:            30,
		NotificationMode:     "errors",
		ServerSideEncryption: "AES256",
	}
	if value != nil {
		if v, ok := value["enabled"].(bool); ok {
			cfg.Enabled = v
		}
		if v, ok := value["include_state"].(bool); ok {
			cfg.IncludeState = v
		}
		if v, ok := value["include_logs"].(bool); ok {
			cfg.IncludeLogs = v
		}
		cfg.Bucket = strings.TrimSpace(config.GetStr(value, "bucket"))
		cfg.Region = strings.TrimSpace(config.GetStr(value, "region"))
		cfg.EndpointURL = strings.TrimRight(strings.TrimSpace(config.GetStr(value, "endpoint_url")), "/")
		cfg.Prefix = strings.Trim(strings.TrimSpace(config.GetStr(value, "prefix")), "/")
		cfg.AddressingStyle = strings.TrimSpace(config.GetStr(value, "addressing_style"))
		cfg.Schedule = strings.TrimSpace(config.GetStr(value, "schedule"))
		cfg.Time = strings.TrimSpace(config.GetStr(value, "time"))
		cfg.NotificationMode = strings.TrimSpace(config.GetStr(value, "notification_mode"))
		cfg.ServerSideEncryption = strings.TrimSpace(config.GetStr(value, "server_side_encryption"))
		for _, field := range []struct {
			key    string
			target *string
		}{
			{"access_key_id", &cfg.AccessKeyID},
			{"secret_access_key", &cfg.SecretAccessKey},
			{"session_token", &cfg.SessionToken},
			{"backup_password", &cfg.BackupPassword},
			{"kms_key_id", &cfg.KMSKeyID},
		} {
			*field.target = strings.TrimSpace(config.GetStr(value, field.key))
		}
		for _, field := range []struct {
			key    string
			target *int
			def    int
		}{
			{"weekday", &cfg.Weekday, 0},
			{"retention", &cfg.Retention, 30},
		} {
			raw, exists := value[field.key]
			if !exists || raw == nil || raw == "" {
				*field.target = field.def
				continue
			}
			switch v := raw.(type) {
			case float64:
				*field.target = int(v)
			case int:
				*field.target = v
			default:
				parsed, err := strconv.Atoi(fmt.Sprintf("%v", raw))
				if err != nil {
					return nil, errf("S3 星期和保留份数必须是整数")
				}
				*field.target = parsed
			}
		}
	}
	// 布林欄位型別檢查（存在但非 bool 即報錯，訊息逐字）
	if value != nil {
		for _, field := range []string{"enabled", "include_state", "include_logs"} {
			if raw, exists := value[field]; exists && raw != nil {
				if _, ok := raw.(bool); !ok {
					return nil, errf("s3_backup.%s 必须是布尔值", field)
				}
			}
		}
	}
	return cfg, nil
}

// ValidateConfig 對應 validate_config：驗證順序與錯誤訊息逐字一致。
// requireReady=true（enabled 或面板測試）時額外要求 bucket/region/密碼。
func ValidateConfig(value map[string]any, requireReady *bool) (*Config, error) {
	cfg, err := NormalizedConfig(value)
	if err != nil {
		return nil, err
	}
	return validateStruct(cfg, requireReady)
}

// validateStruct 對已正規化結構執行驗證（供 NormalizedConfig 之後使用）。
func validateStruct(cfg *Config, requireReady *bool) (*Config, error) {
	switch cfg.Schedule {
	case "hourly", "daily", "weekly":
	default:
		return nil, errf("S3 备份周期必须是 hourly、daily 或 weekly")
	}
	if !timeRe.MatchString(cfg.Time) {
		return nil, errf("S3 备份时间必须是 HH:MM")
	}
	if cfg.Weekday < 0 || cfg.Weekday > 6 {
		return nil, errf("S3 每周备份日期必须在 0 到 6 之间")
	}
	if cfg.Retention < 1 || cfg.Retention > 365 {
		return nil, errf("S3 备份保留份数必须在 1 到 365 之间")
	}
	switch cfg.NotificationMode {
	case "always", "errors", "none":
	default:
		return nil, errf("S3 通知模式必须是 always、errors 或 none")
	}
	switch cfg.ServerSideEncryption {
	case "", "AES256", "aws:kms":
	default:
		return nil, errf("S3 服务端加密必须是 AES256、aws:kms 或关闭")
	}
	switch cfg.AddressingStyle {
	case "auto", "path", "virtual":
	default:
		return nil, errf("S3 寻址方式必须是 auto、path 或 virtual")
	}
	if cfg.ServerSideEncryption == "aws:kms" && cfg.KMSKeyID == "" {
		return nil, errf("使用 SSE-KMS 时必须填写 KMS Key ID")
	}
	if (cfg.AccessKeyID == "") != (cfg.SecretAccessKey == "") {
		return nil, errf("S3 Access Key ID 和 Secret Access Key 必须同时填写或同时留空")
	}
	if cfg.SessionToken != "" && cfg.AccessKeyID == "" {
		return nil, errf("S3 Session Token 需要同时配置 Access Key")
	}
	if cfg.EndpointURL != "" {
		parsed, err := url.Parse(cfg.EndpointURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" || parsed.User != nil {
			return nil, errf("S3 Endpoint 必须是无账号密码的 HTTP/HTTPS 地址")
		}
		if parsed.Scheme == "http" {
			host := parsed.Hostname()
			if host != "127.0.0.1" && host != "localhost" && host != "::1" {
				return nil, errf("远程 S3 Endpoint 必须使用 HTTPS；HTTP 仅允许本机 MinIO")
			}
		}
	}
	if cfg.Prefix != "" {
		if len(cfg.Prefix) > 256 {
			return nil, errf("S3 对象前缀无效")
		}
		for _, part := range strings.Split(cfg.Prefix, "/") {
			if part == "." || part == ".." {
				return nil, errf("S3 对象前缀无效")
			}
		}
	}
	ready := cfg.Enabled
	if requireReady != nil {
		ready = *requireReady
	}
	if ready {
		if cfg.Bucket == "" {
			return nil, errf("S3 Bucket 不能为空")
		}
		if cfg.Region == "" {
			return nil, errf("S3 Region 不能为空")
		}
		if len([]rune(cfg.BackupPassword)) < 8 {
			return nil, errf("S3 自动备份密码至少需要 8 个字符")
		}
	}
	return cfg, nil
}

// ObjectPrefix 對應 object_prefix：prefix + "/"（空 prefix 無斜線）。
func (c *Config) ObjectPrefix() string {
	if c.Prefix == "" {
		return ""
	}
	return c.Prefix + "/"
}

// Secrets 集合供錯誤脫敏。
func (c *Config) Secrets() []string {
	var out []string
	for _, s := range []string{c.AccessKeyID, c.SecretAccessKey, c.SessionToken, c.BackupPassword} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// CompactError 對應 _compact_error：壓平換行、脫敏、截 600 字。
func CompactError(err error, secrets []string) string {
	text := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 600 {
		text = text[:600]
	}
	return text
}
