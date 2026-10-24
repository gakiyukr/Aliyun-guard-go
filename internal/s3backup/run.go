// 備份執行與狀態機，對應 create_and_upload / run_if_due / _read_state /
// _write_state / backup_lock。
package s3backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliyun-guard-go/internal/backup"
	"aliyun-guard-go/internal/s3"
)

// NewS3Client 為套件級工廠變數：測試中替換為 httptest 假客戶端。
var NewS3Client = func(cfg s3.Config) (*s3.Client, error) {
	return s3.New(cfg)
}

// clientConfig 由設定組出 S3 客戶端組態。
func clientConfig(cfg *Config) s3.Config {
	return s3.Config{
		Bucket:          cfg.Bucket,
		Region:          cfg.Region,
		Endpoint:        cfg.EndpointURL,
		AddressingStyle: cfg.AddressingStyle,
		Creds: s3.Credentials{
			AccessKeyID:     cfg.AccessKeyID,
			SecretAccessKey: cfg.SecretAccessKey,
			SessionToken:    cfg.SessionToken,
		},
	}
}

// CreateAndUpload 建立加密備份並上傳（含遠端與本機裁剪），對應
// create_and_upload。回傳結果 dict（ok/key/filename/size/deleted/...）。
func CreateAndUpload(cfg *Config, appDir string, now time.Time) (map[string]any, error) {
	stamp := now.Format("20060102-150405.000000")
	stamp = strings.Replace(stamp, ".", "", 1)
	filename := "aliyun-guard-s3-" + stamp + ".agbackup"
	localPath := filepath.Join(appDir, "backups", filename)

	_, err := backup.CreateBackup(cfg.BackupPassword, appDir, localPath, cfg.IncludeState, cfg.IncludeLogs)
	if err != nil {
		return nil, err
	}
	client, err := NewS3Client(clientConfig(cfg))
	if err != nil {
		return nil, err
	}
	key := cfg.ObjectPrefix() + filename
	body, err := os.ReadFile(localPath)
	if err != nil {
		return nil, err
	}
	meta := map[string]string{"format": backup.Format}
	sse := cfg.ServerSideEncryption
	if err := client.PutObject(context.Background(), key, body, meta, sse, cfg.KMSKeyID); err != nil {
		return nil, errf("上传 S3 加密备份失败: %s", CompactError(err, cfg.Secrets()))
	}
	deleted, err := PruneBackups(cfg, client)
	if err != nil {
		return nil, err
	}
	localDeleted := PruneLocalBackups(appDir, cfg.Retention)
	info, _ := os.Stat(localPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return map[string]any{
		"ok":            true,
		"key":           key,
		"filename":      filename,
		"size":          size,
		"local_path":    localPath,
		"deleted":       deleted,
		"local_deleted": localDeleted,
		"bucket":        cfg.Bucket,
	}, nil
}

// PruneLocalBackups 依 mtime 保留最新 retention 份本機 S3 備份，對應
// prune_local_backups。
func PruneLocalBackups(appDir string, retention int) []string {
	matches, _ := filepath.Glob(filepath.Join(appDir, "backups", "aliyun-guard-s3-*.agbackup"))
	type item struct {
		path  string
		mtime time.Time
	}
	items := make([]item, 0, len(matches))
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil {
			items = append(items, item{path, info.ModTime()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mtime.After(items[j].mtime) })
	deleted := []string{}
	if retention < len(items) {
		for _, expired := range items[retention:] {
			_ = os.Remove(expired.path)
			deleted = append(deleted, expired.path)
		}
	}
	return deleted
}

// readState 讀取 s3-backup-state.json（損壞視為空）。
func readState(appDir string) map[string]any {
	data, err := os.ReadFile(filepath.Join(appDir, StateName))
	if err != nil {
		return map[string]any{}
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil || state == nil {
		return map[string]any{}
	}
	return state
}

// writeState 原子寫入狀態檔（縮排 JSON + 0600，與上游格式一致）。
func writeState(appDir string, state map[string]any) error {
	path := filepath.Join(appDir, StateName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	return os.Rename(tmp, path)
}

// RunIfDue 為套件級函式變數：測試中替換以隔離排程執行。
// 預設實作對應 run_if_due：未啟用/未到期回 nil；slot 已成功回 nil；
// 900 秒重試節流；鎖被占用回 skipped；執行後寫入 last_* 欄位。
var RunIfDue = runIfDueImpl

// runIfDueImpl 為 RunIfDue 的預設實作。
func runIfDueImpl(value map[string]any, appDir string, now time.Time) map[string]any {
	cfg, err := NormalizedConfig(value)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if !cfg.Enabled {
		return nil
	}
	if _, err := validateStruct(cfg, nil); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	slot := ScheduleSlot(cfg, now)
	if slot == "" {
		return nil
	}
	lockFile, locked, err := acquireLock(appDir)
	if err != nil {
		return map[string]any{"ok": false, "error": CompactError(err, nil)}
	}
	if !locked {
		return map[string]any{"ok": true, "skipped": "locked", "slot": slot}
	}
	defer releaseLock(lockFile)

	state := readState(appDir)
	if state["last_success_slot"] == slot {
		return nil
	}
	if state["last_attempt_slot"] == slot {
		if last, err := time.Parse(time.RFC3339, str(state["last_attempt_at"])); err == nil {
			if now.Sub(last) < RetrySeconds*time.Second {
				return nil
			}
		}
	}
	state["last_attempt_slot"] = slot
	state["last_attempt_at"] = now.Format("2006-01-02T15:04:05Z07:00")
	_ = writeState(appDir, state)

	result, err := CreateAndUpload(cfg, appDir, now)
	if err != nil {
		errorText := CompactError(err, cfg.Secrets())
		state["last_error"] = errorText
		_ = writeState(appDir, state)
		return map[string]any{"ok": false, "slot": slot, "error": errorText}
	}
	state["last_success_slot"] = slot
	state["last_success_at"] = now.Format("2006-01-02T15:04:05Z07:00")
	state["last_key"] = result["key"]
	state["last_error"] = nil
	_ = writeState(appDir, state)
	result["slot"] = slot
	return result
}

// ReadStatus 讀取 s3-backup-state.json 原始狀態（供面板 payload；損壞視為空）。
func ReadStatus(appDir string) map[string]any {
	return readState(appDir)
}

// str 安全取字串。
func str(v any) string {
	s, _ := v.(string)
	return s
}
