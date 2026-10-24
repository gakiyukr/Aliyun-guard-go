// Package config 負責 config.json 的載入與保存。
// 為了與 Python 版雙向相容，設定以 map[string]any 為基底：
// 未知欄位（例如已放棄的 proxy 節點設定）會原樣保留，回寫時不流失。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"aliyun-guard-go/internal/xtime"
)

// Paths 指向 config.json 與 state.json，可用環境變數覆寫（與 Python 版一致）。
type Paths struct {
	Config string
	State  string
}

// FromEnv 依 ALIYUN_GUARD_HOME / ALIYUN_GUARD_CONFIG / ALIYUN_GUARD_STATE 解析路徑。
func FromEnv() Paths {
	home := os.Getenv("ALIYUN_GUARD_HOME")
	if home == "" {
		home = "/opt/aliyun-guard"
	}
	cfg := os.Getenv("ALIYUN_GUARD_CONFIG")
	if cfg == "" {
		cfg = filepath.Join(home, "config.json")
	}
	st := os.Getenv("ALIYUN_GUARD_STATE")
	if st == "" {
		st = filepath.Join(home, "state.json")
	}
	return Paths{Config: cfg, State: st}
}

// Default 回傳與 Python 版 DEFAULT_CONFIG 對齊的預設設定。
func Default() map[string]any {
	return map[string]any{
		"version":                2,
		"interval_seconds":       300,
		"billing_cache_seconds":  3600,
		"notification_mode":      "always",
		"notify_on_daemon_start": false,
		"force_ipv4":             true,
		"web_panel": map[string]any{
			"enabled":       false,
			"host":          "127.0.0.1",
			"port":          8765,
			"username":      "admin",
			"password_hash": "",
			"cookie_secure": false,
		},
		"telegram": map[string]any{
			"bot_token":         "",
			"chat_id":           "",
			"timeout_seconds":   12,
			"retries":           3,
			"connection_mode":   "direct",
			"api_base_url":      "https://api.telegram.org",
			"control_enabled":   true,
			"control_admin_ids": []any{},
		},
		"start_wait_seconds": 90,
		"stop_wait_seconds":  45,
		"start_poll_seconds": 5,
		"users":              []any{},
		// s3_backup 預設對齊 s3_backup.DEFAULT_CONFIG（面板/守護共用）。
		"s3_backup": DefaultS3Backup(),
	}
}

// DefaultS3Backup 與 s3_backup.py DEFAULT_CONFIG 逐欄對齊。
func DefaultS3Backup() map[string]any {
	return map[string]any{
		"enabled":                false,
		"include_state":          true,
		"include_logs":           false,
		"bucket":                 "",
		"region":                 "us-east-1",
		"endpoint_url":           "",
		"addressing_style":       "auto",
		"prefix":                 "aliyun-guard",
		"access_key_id":          "",
		"secret_access_key":      "",
		"session_token":          "",
		"backup_password":        "",
		"schedule":               "daily",
		"time":                   "03:00",
		"weekday":                0,
		"retention":              30,
		"notification_mode":      "errors",
		"server_side_encryption": "AES256",
		"kms_key_id":             "",
	}
}

// DefaultBilling 與 Python 版 DEFAULT_BILLING 對齊。
func DefaultBilling() map[string]any {
	return map[string]any{
		"enabled":         true,
		"site":            "china",
		"endpoint":        "business.aliyuncs.com",
		"region":          "cn-hangzhou",
		"currency_code":   "CNY",
		"currency_symbol": "¥",
	}
}

// DefaultSchedule 與 Python 版 DEFAULT_SCHEDULE 對齊；timezone 為 Go 版新增，
// 排程時刻以此時區解讀（預設 Asia/Taipei），不再依賴部署機器的系統時區。
func DefaultSchedule() map[string]any {
	return map[string]any{
		"enabled":    false,
		"start_time": "08:00",
		"stop_time":  "23:00",
		"timezone":   xtime.ScheduleDefaultZone,
	}
}

// DeepMerge 以 src 遞迴覆蓋 dst 中的巢狀 map，其餘型別直接替換。
func DeepMerge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				dst[k] = DeepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

// Load 讀取 config.json；檔案不存在時回傳預設值。回傳值為合併預設後的結果。
func Load(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config.json 解析失败: %w", err)
	}
	return DeepMerge(Default(), cfg), nil
}

// Save 以 0600 權限原子寫入 config.json（先寫暫存檔再 rename），並收緊
// 既有檔案與目錄權限（Python 版可能留下 0644/0755）——檔內含明文 AK/SK。
func Save(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	_ = os.Chmod(filepath.Dir(path), 0o700)
	return nil
}

// Users 取出 users 陣列，每個元素皆為 map。
func Users(cfg map[string]any) []map[string]any {
	list, _ := cfg["users"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// BillingConfig 對應 Python 版 get_billing_config()：
// 優先使用 user["billing"]，否則相容舊版欄位名（bill_endpoint 等）。
func BillingConfig(user map[string]any) map[string]any {
	if configured, ok := user["billing"].(map[string]any); ok {
		return DeepMerge(DefaultBilling(), configured)
	}
	endpoint := GetStr(user, "bill_endpoint")
	if endpoint == "" {
		endpoint = "business.aliyuncs.com"
	}
	international := endpoint != "business.aliyuncs.com"
	legacy := map[string]any{
		"enabled":         GetBool(user, "billing_enabled", true),
		"site":            "china",
		"endpoint":        endpoint,
		"region":          "cn-hangzhou",
		"currency_code":   "CNY",
		"currency_symbol": "¥",
	}
	if international {
		legacy["site"] = "international"
		legacy["region"] = "ap-southeast-1"
		legacy["currency_code"] = "USD"
		symbol := GetStr(user, "currency")
		if symbol == "" {
			symbol = "$"
		}
		legacy["currency_symbol"] = symbol
	}
	return DeepMerge(DefaultBilling(), legacy)
}

// --- map 存取輔助：比照 Python dict.get 的寬鬆語意 ---

func GetStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func GetInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func GetFloat(m map[string]any, key string, def float64) float64 {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func GetBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func GetMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	mm, _ := m[key].(map[string]any)
	return mm
}

func GetSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	s, _ := m[key].([]any)
	return s
}
