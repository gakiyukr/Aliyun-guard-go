// M4 per-instance 日誌測試：路徑命名、欄位格式、脫敏。
package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliyun-guard-go/internal/config"
)

func instanceTestUser() map[string]any {
	return map[string]any{
		"name": "HK-CDT", "ak": "LTAI5tSECRETKEY", "sk": "sksecret99",
		"region": "cn-hongkong", "instance_id": "i-bp1abc123",
		"instance_log_enabled": true,
		"billing":              map[string]any{"enabled": true, "currency_symbol": "¥", "currency_code": "CNY"},
		"traffic_limit_gb":     180.0,
	}
}

func TestInstanceLogPath(t *testing.T) {
	paths := config.Paths{Config: filepath.Join(t.TempDir(), "config.json")}
	user := instanceTestUser()
	path := InstanceLogPath(paths, user)
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "i-bp1abc123-") || !strings.HasSuffix(base, ".log") {
		t.Fatalf("路徑格式不符: %s", path)
	}
	if filepath.Base(filepath.Dir(path)) != "instances" {
		t.Fatalf("應位於 instances 子目錄: %s", path)
	}
	// 同 (region, instance_id) 應產生相同路徑
	if again := InstanceLogPath(paths, user); again != path {
		t.Fatal("相同實例應產生相同路徑")
	}
}

func TestWriteInstanceLog(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(CloseInstanceLogs)
	paths := config.Paths{Config: filepath.Join(dir, "config.json")}
	user := instanceTestUser()
	traffic := 12.34
	ok := WriteInstanceLog(paths, user, InstanceLogEntry{
		Name: "HK-CDT", InstanceID: "i-bp1abc123",
		TrafficGB: &traffic, LimitGB: 180,
		StatusBefore: "Stopped", StatusAfter: "Running",
		BillingEnabled: true, BillingChecked: true,
		Action: "start", ActionPerformed: true,
		Level: "action", Message: "已提交保活启动请求",
	}, false, "周期检测")
	if !ok {
		t.Fatal("啟用獨立日誌時應寫入")
	}
	// 未啟用時不寫
	user2 := instanceTestUser()
	user2["instance_log_enabled"] = false
	if WriteInstanceLog(paths, user2, InstanceLogEntry{}, false, "周期检测") {
		t.Fatal("未啟用時不應寫入")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "instances", "*.log"))
	if len(files) != 1 {
		t.Fatalf("應產生 1 個日誌檔，得到 %v", files)
	}
	data, _ := os.ReadFile(files[0])
	line := string(data)
	for _, want := range []string{
		" - INFO - ", "事件=周期检测", "实例=HK-CDT (i-bp1abc123)", "流量=12.34/180.00 GB",
		"ECS=Stopped->Running", "已执行=是", "演练=否", "说明=已提交保活启动请求",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("日誌行缺少 %q: %s", want, line)
		}
	}
}

func TestInstanceLogRedaction(t *testing.T) {
	user := instanceTestUser()
	redacted := instanceLogValue("key LTAI5tSECRETKEY in https://example.com/a and 1234567890:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw token", user)
	for _, secret := range []string{"LTAI5tSECRETKEY", "https://example.com/a", "1234567890:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("脫敏失敗，仍含 %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "***") || !strings.Contains(redacted, "[链接已隐藏]") ||
		!strings.Contains(redacted, "[Bot Token 已隐藏]") {
		t.Fatalf("應含脫敏佔位: %s", redacted)
	}
	// 截斷
	if got := instanceLogValue("0123456789", user, 5); got != "01234" {
		t.Fatalf("截斷結果不符: %s", got)
	}
	// 空值
	if got := instanceLogValue("", user); got != "-" {
		t.Fatalf("空值應為 -: %s", got)
	}
}
