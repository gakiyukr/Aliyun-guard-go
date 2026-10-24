package s3backup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliyun-guard-go/internal/s3"
)

// validMap 為已啟用且完備的設定（供測試基準）。
func validMap() map[string]any {
	return map[string]any{
		"enabled":                true,
		"bucket":                 "bkt",
		"region":                 "us-east-1",
		"prefix":                 "aliyun-guard",
		"access_key_id":          "ak",
		"secret_access_key":      "sk",
		"backup_password":        "password-8",
		"schedule":               "daily",
		"time":                   "03:00",
		"retention":              2,
		"notification_mode":      "errors",
		"server_side_encryption": "AES256",
		"addressing_style":       "auto",
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestValidateConfigErrors 驗證逐字錯誤訊息。
func TestValidateConfigErrors(t *testing.T) {
	cases := []struct {
		mutate func(map[string]any)
		want   string
	}{
		{func(m map[string]any) { m["schedule"] = "monthly" }, "S3 备份周期必须是 hourly、daily 或 weekly"},
		{func(m map[string]any) { m["time"] = "3:00" }, "S3 备份时间必须是 HH:MM"},
		{func(m map[string]any) { m["weekday"] = 7 }, "S3 每周备份日期必须在 0 到 6 之间"},
		{func(m map[string]any) { m["retention"] = 0 }, "S3 备份保留份数必须在 1 到 365 之间"},
		{func(m map[string]any) { m["retention"] = "x" }, "S3 星期和保留份数必须是整数"},
		{func(m map[string]any) { m["notification_mode"] = "all" }, "S3 通知模式必须是 always、errors 或 none"},
		{func(m map[string]any) { m["server_side_encryption"] = "AES128" }, "S3 服务端加密必须是 AES256、aws:kms 或关闭"},
		{func(m map[string]any) { m["server_side_encryption"] = "aws:kms" }, "使用 SSE-KMS 时必须填写 KMS Key ID"},
		{func(m map[string]any) { m["addressing_style"] = "auto2" }, "S3 寻址方式必须是 auto、path 或 virtual"},
		{func(m map[string]any) { m["secret_access_key"] = "" }, "S3 Access Key ID 和 Secret Access Key 必须同时填写或同时留空"},
		{func(m map[string]any) {
			m["session_token"] = "tok"
			m["secret_access_key"] = ""
			m["access_key_id"] = ""
		}, "S3 Session Token 需要同时配置 Access Key"},
		{func(m map[string]any) { m["endpoint_url"] = "ftp://x" }, "S3 Endpoint 必须是无账号密码的 HTTP/HTTPS 地址"},
		{func(m map[string]any) { m["endpoint_url"] = "http://example.com:9000" }, "远程 S3 Endpoint 必须使用 HTTPS；HTTP 仅允许本机 MinIO"},
		{func(m map[string]any) { m["prefix"] = "a/../b" }, "S3 对象前缀无效"},
		{func(m map[string]any) { m["bucket"] = "" }, "S3 Bucket 不能为空"},
		{func(m map[string]any) { m["backup_password"] = "short" }, "S3 自动备份密码至少需要 8 个字符"},
		{func(m map[string]any) { m["enabled"] = "yes" }, "s3_backup.enabled 必须是布尔值"},
	}
	for _, c := range cases {
		m := validMap()
		c.mutate(m)
		_, err := ValidateConfig(m, nil)
		if err == nil || err.Error() != c.want {
			t.Errorf("錯誤不符: got %v, want %s", err, c.want)
		}
	}
}

// TestScheduleSlot 驗證三種週期的 slot 格式與未到期語意。
func TestScheduleSlot(t *testing.T) {
	base := time.Date(2026, 9, 9, 10, 30, 0, 0, time.UTC) // 週三
	daily := &Config{Schedule: "daily", Time: "09:00"}
	if got := ScheduleSlot(daily, base); got != "daily:20260909" {
		t.Errorf("daily slot 不符: %q", got)
	}
	early := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	if got := ScheduleSlot(daily, early); got != "" {
		t.Errorf("未到時間應為空: %q", got)
	}
	hourly := &Config{Schedule: "hourly", Time: "30"}
	if got := ScheduleSlot(hourly, base); got != "hourly:2026090910" {
		t.Errorf("hourly slot 不符: %q", got)
	}
	weekly := &Config{Schedule: "weekly", Time: "09:00", Weekday: 1}
	// 週一 = 9/7；目標 = 9/8 09:00 → base（9/9 10:30）已過
	if got := ScheduleSlot(weekly, base); got != "weekly:20260907" {
		t.Errorf("weekly slot 不符: %q", got)
	}
	// 目標未到（9/7 當天 08:00）→ 空
	beforeTarget := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	if got := ScheduleSlot(weekly, beforeTarget); got != "" {
		t.Errorf("weekly 未到應為空: %q", got)
	}
}

// fakeS3 啟動 httptest 假 S3 並注入 NewS3Client stub。
func fakeS3(t *testing.T, keys []string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/") {
			fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		w.WriteHeader(200)
	}))
	srvURL := srv.URL
	orig := NewS3Client
	NewS3Client = func(cfg s3.Config) (*s3.Client, error) {
		cfg.Endpoint = srvURL
		cfg.AddressingStyle = "path"
		return s3.New(cfg)
	}
	t.Cleanup(func() {
		NewS3Client = orig
		srv.Close()
	})
	_ = keys
}

// TestRunIfDueStateMachine 驗證完整流程：首次執行 → 成功狀態 → 同 slot 跳過。
func TestRunIfDueStateMachine(t *testing.T) {
	fakeS3(t, nil)
	appDir := t.TempDir()
	writeTestFile(t, filepath.Join(appDir, "config.json"), `{"users":[]}`)

	now := time.Date(2026, 9, 9, 10, 30, 0, 0, time.UTC)
	result := RunIfDue(validMap(), appDir, now)
	if result == nil || result["ok"] != true {
		t.Fatalf("首次執行應成功: %v", result)
	}
	if result["slot"] != "daily:20260909" {
		t.Errorf("slot 不符: %v", result)
	}
	// 上傳後本機應有備份檔
	matches, _ := filepath.Glob(filepath.Join(appDir, "backups", "aliyun-guard-s3-*.agbackup"))
	if len(matches) != 1 {
		t.Fatalf("本機應有 1 份備份，實得 %d", len(matches))
	}
	// 同 slot 第二次 → nil（last_success_slot 命中）
	if result := RunIfDue(validMap(), appDir, now.Add(5*time.Minute)); result != nil {
		t.Errorf("同 slot 應跳過: %v", result)
	}
	// 狀態檔欄位齊全
	data, _ := os.ReadFile(filepath.Join(appDir, StateName))
	var state map[string]any
	_ = json.Unmarshal(data, &state)
	if state["last_success_slot"] != "daily:20260909" || state["last_key"] == nil || state["last_error"] != nil {
		t.Errorf("狀態檔不符: %v", state)
	}
}

// TestRunIfDueDisabled 未啟用回 nil。
func TestRunIfDueDisabled(t *testing.T) {
	m := validMap()
	m["enabled"] = false
	if result := RunIfDue(m, t.TempDir(), time.Now()); result != nil {
		t.Errorf("未啟用應回 nil: %v", result)
	}
}

// TestRunIfDueUploadFailure 上傳失敗 → last_error 記錄且 ok=false。
func TestRunIfDueUploadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
	}))
	srvURL := srv.URL
	orig := NewS3Client
	NewS3Client = func(cfg s3.Config) (*s3.Client, error) {
		cfg.Endpoint = srvURL
		cfg.AddressingStyle = "path"
		return s3.New(cfg)
	}
	t.Cleanup(func() { NewS3Client = orig; srv.Close() })

	appDir := t.TempDir()
	writeTestFile(t, filepath.Join(appDir, "config.json"), `{"users":[]}`)
	now := time.Date(2026, 9, 9, 10, 30, 0, 0, time.UTC)
	result := RunIfDue(validMap(), appDir, now)
	if result == nil || result["ok"] != false {
		t.Fatalf("上傳失敗應 ok=false: %v", result)
	}
	if !strings.Contains(result["error"].(string), "上传 S3 加密备份失败") {
		t.Errorf("錯誤應帶標籤: %v", result)
	}
	if !strings.Contains(result["error"].(string), "AccessDenied") {
		t.Errorf("錯誤應含 S3 錯誤碼: %v", result)
	}
	// 狀態檔 last_error 已記錄；900 秒內重試被節流
	if result := RunIfDue(validMap(), appDir, now.Add(time.Minute)); result != nil {
		t.Errorf("節流期內應回 nil: %v", result)
	}
}
