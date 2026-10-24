// 備份檔案收集與建立，對應 collect_data_files / create_backup。
// 收集順序、大小限制與錯誤訊息逐字對齊上游。
package backup

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// readFileCapped 讀取檔案，超過加密檔上限即拒讀（防記憶體耗盡）。
func readFileCapped(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxBackupFileBytes {
		return nil, errf("文件过大，未加入备份: %s", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// CollectDataFiles 收集備份來源檔，回傳 名稱 → {content(b64), sha256, mode}。
// 必收 config.json 與 service_backend（存在者）；include_state 加 state.json
// 與 telegram-control-state.json；include_logs 遞迴收 logs/**。
func CollectDataFiles(appDir string, includeState, includeLogs bool) (map[string]fileEntry, error) {
	files := map[string]fileEntry{}
	totalSize := 0

	addFile := func(name, path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > MaxFileBytes {
			return errf("文件过大，未加入备份: %s", filepath.Base(path))
		}
		totalSize += len(data)
		if totalSize > MaxBackupSourceBytes {
			return errf("备份源文件超过 %d MiB 限制", MaxBackupSourceBytes/1048576)
		}
		mode := 0o600
		if info, err := os.Stat(path); err == nil {
			mode = int(info.Mode().Perm())
		}
		sum := sha256.Sum256(data)
		files[name] = fileEntry{
			Content: base64.StdEncoding.EncodeToString(data),
			SHA256:  hex.EncodeToString(sum[:]),
			Mode:    mode,
		}
		return nil
	}

	selected := []string{"config.json", "service_backend"}
	if includeState {
		selected = append(selected, "state.json", "telegram-control-state.json")
	}
	for _, name := range selected {
		path := filepath.Join(appDir, filepath.FromSlash(name))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			if err := addFile(name, path); err != nil {
				return nil, err
			}
		}
	}
	if includeLogs {
		logDir := filepath.Join(appDir, "logs")
		var logFiles []string
		_ = filepath.WalkDir(logDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			logFiles = append(logFiles, path)
			return nil
		})
		sort.Strings(logFiles)
		for _, path := range logFiles {
			rel, err := filepath.Rel(logDir, path)
			if err != nil {
				continue
			}
			if err := addFile("logs/"+filepath.ToSlash(rel), path); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

// CreateBackup 建立加密備份檔（原子寫入目的地），對應 create_backup。
// 回傳目的地路徑。passphrase 強度與 config.json 存在性在此驗證。
func CreateBackup(passphrase, appDir, outputPath string, includeState, includeLogs bool) (string, error) {
	files, err := CollectDataFiles(appDir, includeState, includeLogs)
	if err != nil {
		return "", err
	}
	if _, ok := files["config.json"]; !ok {
		return "", errf("未找到 config.json，无法创建完整备份")
	}
	payload := map[string]any{
		"format":     Format,
		"created_at": nowText(),
		"source":     "Aliyun Guard",
		"files":      files,
	}
	destination := outputPath
	if destination == "" {
		destination = filepath.Join(appDir, "backups", "aliyun-guard-"+stamp(time.Now())+".agbackup")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", err
	}
	envelope, err := EncryptPayload(payload, passphrase, PBKDF2Iterations)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(destination, envelope, 0o600); err != nil {
		return "", err
	}
	return destination, nil
}

// writeFileAtomic 經同目錄暫存檔 + rename 原子寫入（與 config.Save 同模式）。
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// stamp 對應上游 _stamp：YYYYMMDD-HHMMSS 本地時間。
func stamp(t time.Time) string {
	return t.Format("20060102-150405")
}
