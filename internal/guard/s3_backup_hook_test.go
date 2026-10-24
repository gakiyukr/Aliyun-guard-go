// S3 備份守護掛點測試：notification_mode 三種模式的通知行為與訊息逐字對齊。
package guard

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/s3backup"
)

// s3backupRunIfDueOrig 保存原始 RunIfDue 供測試後還原。
var s3backupRunIfDueOrig = s3backup.RunIfDue

// TestS3BackupCheckNotifyModes 驗證 always/errors/none 三種通知模式、
// skipped/nil 靜默，以及成功/失敗訊息逐字前綴。
func TestS3BackupCheckNotifyModes(t *testing.T) {
	cases := []struct {
		mode   string
		result map[string]any
		want   string
		sent   bool
	}{
		{"always", map[string]any{
			"ok": true, "key": "p/x.agbackup", "bucket": "bkt", "size": int64(2 * 1048576),
			"deleted": []string{"a", "b"},
		}, "Aliyun Guard S3 自动备份成功", true},
		{"errors", map[string]any{
			"ok": true, "key": "p/x.agbackup", "bucket": "bkt", "size": int64(1048576),
			"deleted": []string{},
		}, "Aliyun Guard S3 自动备份成功", false},
		{"none", map[string]any{
			"ok": true, "key": "p/x.agbackup", "bucket": "bkt", "size": int64(1048576),
			"deleted": []string{},
		}, "Aliyun Guard S3 自动备份成功", false},
		{"always", map[string]any{"ok": false, "error": "boom"},
			"Aliyun Guard S3 自动备份失败", true},
		{"errors", map[string]any{"ok": false, "error": "boom"},
			"Aliyun Guard S3 自动备份失败", true},
		{"none", map[string]any{"ok": false, "error": "boom"},
			"Aliyun Guard S3 自动备份失败", false},
	}
	for _, c := range cases {
		var sent []string
		s3backup.RunIfDue = func(value map[string]any, appDir string, now time.Time) map[string]any {
			return c.result
		}
		orig := notify.SendMessageFunc
		notify.SendMessageFunc = func(cfg map[string]any, text string) error {
			sent = append(sent, text)
			return nil
		}
		cfg := map[string]any{
			"s3_backup": map[string]any{"notification_mode": c.mode},
			"telegram":  map[string]any{"bot_token": "123:abc", "chat_id": 1},
		}
		runS3BackupCheck(cfg, config.Paths{Config: filepath.Join(t.TempDir(), "config.json")}, time.Now())
		notify.SendMessageFunc = orig
		if gotSent := len(sent) > 0; gotSent != c.sent {
			t.Errorf("mode=%s 應送=%v 實得=%v", c.mode, c.sent, gotSent)
		}
		if len(sent) > 0 && !strings.HasPrefix(sent[0], c.want) {
			t.Errorf("訊息前綴不符: %q", sent[0])
		}
	}
	// skipped / nil 應靜默
	for _, result := range []map[string]any{nil, {"ok": true, "skipped": "locked"}} {
		var sent []string
		s3backup.RunIfDue = func(value map[string]any, appDir string, now time.Time) map[string]any {
			return result
		}
		orig := notify.SendMessageFunc
		notify.SendMessageFunc = func(cfg map[string]any, text string) error {
			sent = append(sent, text)
			return nil
		}
		runS3BackupCheck(map[string]any{"s3_backup": map[string]any{"notification_mode": "always"}},
			config.Paths{}, time.Now())
		notify.SendMessageFunc = orig
		if len(sent) != 0 {
			t.Errorf("result=%v 應靜默", result)
		}
	}
	s3backup.RunIfDue = s3backupRunIfDueOrig
}
