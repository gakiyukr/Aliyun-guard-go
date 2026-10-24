// 守護迴圈的 S3 備份到期檢查，對應 run_s3_backup（aliyun_guard.py
// 2743-2779）：執行 run_if_due 並依 notification_mode 發送 Telegram 通知，
// 成功/失敗訊息逐字對齊上游。
package guard

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/s3backup"
)

// s3BackupCheck 為套件級函式變數：測試中替換以隔離 S3 排程執行。
var s3BackupCheck = runS3BackupCheck

// runS3BackupCheck 執行 S3 到期檢查並通知（停用/未到期/同 slot 由
// s3backup.RunIfDue 回 nil 靜默跳過）。
func runS3BackupCheck(cfg map[string]any, paths config.Paths, now time.Time) {
	value := config.GetMap(cfg, "s3_backup")
	result := s3backup.RunIfDue(value, filepath.Dir(paths.Config), now)
	if result == nil || result["skipped"] != nil {
		return
	}
	mode := config.GetStr(value, "notification_mode")
	secrets := s3SecretsFromMap(value)
	var shouldSend bool
	var text string
	if result["ok"] == true {
		key, _ := result["key"].(string)
		bucket, _ := result["bucket"].(string)
		size, _ := result["size"].(int64)
		deleted, _ := result["deleted"].([]string)
		Logger.Printf("S3 自动备份成功: %s，清理 %d 份旧备份", key, len(deleted))
		shouldSend = mode == "always"
		text = fmt.Sprintf(
			"Aliyun Guard S3 自动备份成功\n时间: %s\nBucket: %s\n对象: %s\n大小: %.2f MiB\n清理旧备份: %d 份",
			now.Format("2006-01-02 15:04:05"), bucket, key,
			float64(size)/1048576, len(deleted),
		)
	} else {
		errText, _ := result["error"].(string)
		if errText == "" {
			errText = "未知错误"
		}
		Logger.Errorf("S3 自动备份失败: %s", redactSecrets(errText, secrets))
		shouldSend = mode == "always" || mode == "errors"
		text = fmt.Sprintf(
			"Aliyun Guard S3 自动备份失败\n时间: %s\n错误: %s",
			now.Format("2006-01-02 15:04:05"), redactSecrets(errText, secrets),
		)
	}
	if shouldSend {
		if err := notify.SendMessage(cfg, text); err != nil {
			Logger.Errorf("S3 备份结果 Telegram 通知失败: %s", redactSecrets(err.Error(), secrets))
		}
	}
}

// s3SecretsFromMap 集合 S3 設定中的機密供錯誤脫敏。
func s3SecretsFromMap(value map[string]any) []string {
	var secrets []string
	for _, field := range []string{"access_key_id", "secret_access_key", "session_token", "backup_password"} {
		if s := config.GetStr(value, field); s != "" {
			secrets = append(secrets, s)
		}
	}
	return secrets
}

// redactSecrets 將機密替換為 ***（上游 compact_error 的脫敏部分）。
func redactSecrets(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text
}
