// 備份鎖（非 Windows）：fcntl.flock 非阻塞獨佔鎖，對應 backup_lock。
//go:build !windows

package s3backup

import (
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock 嘗試取得 s3-backup.lock；取得成功回傳檔案與 true，
// 被占用回傳 (nil, false)。呼叫端以 releaseLock 釋放。
func acquireLock(appDir string) (*os.File, bool, error) {
	path := filepath.Join(appDir, LockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false, nil
	}
	return f, true, nil
}

// releaseLock 解鎖並關閉鎖檔。
func releaseLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
