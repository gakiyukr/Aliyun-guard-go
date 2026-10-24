// 備份套件：與 Python 版 backup_manager.py 互通的加密備份信封、檔案收集
// 與還原。信封格式 aliyun-guard-backup-v1 為跨版本契約：AES-256-GCM +
// PBKDF2-HMAC-SHA256（手寫，零依賴），JSON 信封八欄位，AAD 綁定格式字串
// 與迭代數。所有面向用戶的錯誤訊息與上游逐字一致。
package backup

import (
	"errors"
	"fmt"
)

// 格式與上限常數，逐項對應 backup_manager.py。
const (
	Format = "aliyun-guard-backup-v1"

	PBKDF2Iterations = 240000
	MinIterations    = 100000
	MaxIterations    = 2000000

	MaxFileBytes         = 16 * 1024 * 1024 // 單檔收集上限
	MaxBackupBytes       = 64 * 1024 * 1024 // 明文 payload 上限
	MaxBackupFileBytes   = 88 * 1024 * 1024 // 加密後檔案上限
	MaxBackupSourceBytes = 45 * 1024 * 1024 // 收集源合計上限
)

// dataFiles 為還原白名單中的資料檔（對應上游 DATA_FILES；service_backend
// 為 Python 版專屬，Go 版目錄不存在時收集自然跳過，還原仍允許以保持互通）。
var dataFiles = map[string]bool{
	"config.json":                 true,
	"state.json":                  true,
	"telegram-control-state.json": true,
	"s3-backup-state.json":        true,
	"service_backend":             true,
}

// Error 對應上游 BackupError：訊息即面向用戶的逐字中文。
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// errf 組裝 BackupError。
func errf(format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

// AsError 供呼叫端以 errors.As 判別備份語意錯誤（如面板映射 400）。
func AsError(err error) (*Error, bool) {
	var be *Error
	ok := errors.As(err, &be)
	return be, ok
}
