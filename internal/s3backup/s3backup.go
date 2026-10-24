// S3 備份編排套件，對應 Python 版 s3_backup.py：設定正規化與驗證（逐字
// 錯誤）、排程 slot、遠端清單/清理/下載、create_and_upload 與 run_if_due
// 狀態機（s3-backup-state.json、flock 鎖、900 秒重試節流）。
package s3backup

import (
	"errors"
	"fmt"
)

// 常數對應上游。
const (
	StateName      = "s3-backup-state.json"
	LockName       = "s3-backup.lock"
	MaxRemoteItems = 5000
	RetrySeconds   = 15 * 60
)

// Error 對應上游 S3BackupError：訊息為面向用戶的逐字中文。
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func errf(format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

// AsError 供呼叫端以 errors.As 判別備份語意錯誤。
func AsError(err error) (*Error, bool) {
	var se *Error
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
