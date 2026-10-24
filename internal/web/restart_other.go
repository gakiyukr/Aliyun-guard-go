// 服務重啟的非 Linux 實作：僅記錄跳過訊息（開發環境不執行重啟）。
//go:build !linux

package web

import "aliyun-guard-go/internal/guard"

// serviceRestart 非 Linux 平台不支援服務重啟，僅記錄日誌。
func serviceRestart(appDir string) error {
	guard.Logger.Warningf("Web requested restart skipped (non-linux)")
	return nil
}
