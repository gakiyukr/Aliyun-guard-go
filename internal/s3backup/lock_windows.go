// 備份鎖（Windows 開發機）：以檔案開啟獨佔性近似 flock 語意；
// 生產環境為 Linux，走 lock_unix.go。
//go:build windows

package s3backup

import (
	"os"
	"path/filepath"
)

// acquireLock Windows 版以排他開啟近似鎖（開發環境單行程，語意足夠）。
func acquireLock(appDir string) (*os.File, bool, error) {
	path := filepath.Join(appDir, LockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, false, err
	}
	return f, true, nil
}

// releaseLock 關閉鎖檔。
func releaseLock(f *os.File) {
	_ = f.Close()
}
