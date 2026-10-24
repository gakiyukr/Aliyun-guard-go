// 備份還原與預覽，對應 preview_restore / restore_backup / _safe_relative_path。
// 還原路徑白名單為安全關鍵：僅接受 DATA_FILES 與 logs/ 前綴，逐字錯誤。
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SafeRelativePath 驗證備份內檔名：拒絕絕對路徑、..、前導 .；白名單為
// 資料檔或 logs/ 前綴。錯誤訊息逐字對應上游。
func SafeRelativePath(value string) (string, error) {
	text := strings.ReplaceAll(value, "\\", "/")
	text = strings.Trim(text, "/")
	if text == "" ||
		filepath.IsAbs(text) ||
		containsDotDot(text) ||
		strings.HasPrefix(text, ".") {
		return "", errf("备份包含不安全路径")
	}
	if !dataFiles[text] && !strings.HasPrefix(text, "logs/") {
		return "", errf("备份包含不允许恢复的文件: %s", text)
	}
	return text, nil
}

// containsDotDot 檢查路徑任一段是否為 ..。
func containsDotDot(text string) bool {
	for _, part := range strings.Split(text, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// RestorePreview 對應 _preview_payload 的回傳結構。
type RestorePreview struct {
	CreatedAt string         `json:"created_at"`
	Files     []PreviewFile  `json:"files"`
	Summary   map[string]any `json:"summary"`
}

// PreviewFile 為單檔的還原動作。
type PreviewFile struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Size   int    `json:"size"`
}

// PreviewRestore 解密備份並逐檔驗 sha256、比對現況得出動作，對應
// preview_restore。config.json 摘要供面板顯示。
func PreviewRestore(path, passphrase, appDir string) (*RestorePreview, error) {
	payload, err := LoadBackup(path, passphrase)
	if err != nil {
		return nil, err
	}
	return previewPayload(payload, appDir)
}

// previewPayload 對應 _preview_payload：排序檔名、驗 sha256、判動作。
func previewPayload(payload map[string]any, appDir string) (*RestorePreview, error) {
	entries := filesMap(payload)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	preview := &RestorePreview{Summary: map[string]any{}}
	if createdAt, ok := payload["created_at"].(string); ok {
		preview.CreatedAt = createdAt
	}
	for _, rawName := range names {
		entry := entries[rawName]
		name, err := SafeRelativePath(rawName)
		if err != nil {
			return nil, err
		}
		data, err := entryData(rawName, entry)
		if err != nil {
			return nil, err
		}
		current := filepath.Join(appDir, filepath.FromSlash(name))
		action := "add"
		if currentData, err := os.ReadFile(current); err == nil {
			sum := sha256.Sum256(currentData)
			if hex.EncodeToString(sum[:]) == strings.ToLower(entry.SHA256) {
				action = "unchanged"
			} else {
				action = "replace"
			}
		}
		preview.Files = append(preview.Files, PreviewFile{Path: name, Action: action, Size: len(data)})
		if name == "config.json" {
			preview.Summary = configSummary(data)
		}
	}
	return preview, nil
}

// configSummary 對應 _config_summary：解析備份內 config.json 的概況。
func configSummary(content []byte) map[string]any {
	var cfg map[string]any
	if err := json.Unmarshal(content, &cfg); err != nil {
		return map[string]any{"valid_json": false, "instances": 0, "nodes": 0}
	}
	instances, nodes := 0, 0
	if users, ok := cfg["users"].([]any); ok {
		instances = len(users)
	}
	if telegram, ok := cfg["telegram"].(map[string]any); ok {
		if urls, ok := telegram["node_urls"].([]any); ok {
			nodes = len(urls)
		}
	}
	webEnabled := false
	if panel, ok := cfg["web_panel"].(map[string]any); ok {
		if enabled, ok := panel["enabled"].(bool); ok {
			webEnabled = enabled
		}
	}
	return map[string]any{
		"valid_json":  true,
		"instances":   instances,
		"nodes":       nodes,
		"web_enabled": webEnabled,
	}
}

// RestoreBackup 還原備份：驗 config.json 存在於備份、先做安全備份、
// 逐檔原子覆寫（include_logs=false 跳過 logs/）。對應 restore_backup。
// Go 版無上游的 validate_config 全量驗證，僅確認備份內 config.json 為
// JSON 物件（結構層面由守護迴圈熱重載兜底）——已知近似記入 PLAN.md。
func RestoreBackup(path, passphrase, appDir string, includeLogs bool) (map[string]any, error) {
	payload, err := LoadBackup(path, passphrase)
	if err != nil {
		return nil, err
	}
	preview, err := previewPayload(payload, appDir)
	if err != nil {
		return nil, err
	}
	entries := filesMap(payload)
	configEntry, ok := entries["config.json"]
	if !ok {
		return nil, errf("备份缺少 config.json")
	}
	configData, err := entryData("config.json", configEntry)
	if err != nil {
		return nil, err
	}
	var configValue map[string]any
	if err := json.Unmarshal(configData, &configValue); err != nil {
		return nil, errf("备份配置校验失败: %v", err)
	}

	safety, err := CreateBackup(passphrase, appDir,
		filepath.Join(appDir, "backups", "before-restore-"+stamp(time.Now())+".agbackup"),
		true, includeLogs)
	if err != nil {
		return nil, err
	}

	restored := []string{}
	for _, rawName := range sortedKeys(entries) {
		entry := entries[rawName]
		name, err := SafeRelativePath(rawName)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(name, "logs/") && !includeLogs {
			continue
		}
		data, err := entryData(rawName, entry)
		if err != nil {
			return nil, err
		}
		destination := filepath.Join(appDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return nil, err
		}
		mode := entry.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := writeFileAtomic(destination, data, os.FileMode(mode)&0o777); err != nil {
			return nil, err
		}
		restored = append(restored, name)
	}
	return map[string]any{
		"preview":       preview,
		"restored":      restored,
		"safety_backup": safety,
	}, nil
}

// sortedKeys 排序備份檔名（與上游 sorted(payload["files"].items()) 一致）。
func sortedKeys(entries map[string]fileEntry) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
