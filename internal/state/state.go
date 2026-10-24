// Package state 負責 state.json 與 heartbeat.json 的讀寫。
// 結構與 Python 版完全相容：時間戳採「ISO 字串 + epoch 秒」雙格式並存。
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"aliyun-guard-go/internal/xtime"
)

// mu 序列化同一程序內的狀態寫入，對應 Python 版 _JSON_WRITE_LOCK。
var mu sync.Mutex

// Load 讀取 state.json；檔案不存在時回傳空狀態。
func Load(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("state.json 解析失败: %w", err)
	}
	if st == nil {
		st = map[string]any{}
	}
	return st, nil
}

// Save 以 0600 權限原子寫入 state.json，並收緊既有檔案與目錄權限。
func Save(path string, st map[string]any) error {
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
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

// WriteHeartbeat 對應 Python 版 write_heartbeat()：記錄守護行程心跳供看門狗檢查。
// 心跳檔記錄本地時區 ISO 時間與 epoch 秒（雙格式）。
func WriteHeartbeat(path, status, detail string) error {
	now := time.Now()
	data := map[string]any{
		"status": status,
		"detail": detail,
		"at":     now.Format(xtime.ISOFormat),
		"epoch":  now.Unix(),
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
