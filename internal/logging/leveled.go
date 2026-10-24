// LeveledLogger 為分級日誌器，對應 Python 版 LOGGER.info/warning/error 的
// 三級語意：每行輸出「2006-01-02 15:04:05 - LEVEL - 訊息」前綴（與
// PrefixWriter 同一 Formatter 格式），等級由訊息來源呼叫的方法決定，
// 而非像 PrefixWriter 固定單一等級。
package logging

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// LeveledLogger 將分級後的行寫入所有下游 Writer（輪轉檔、終端等）。
// 執行緒安全：各等級輸出共用一把鎖，避免多行交錯。
type LeveledLogger struct {
	mu      sync.Mutex
	writers []io.Writer
}

// NewLeveled 建立分級日誌器，寫入所有傳入的下游 Writer。
func NewLeveled(writers ...io.Writer) *LeveledLogger {
	return &LeveledLogger{writers: writers}
}

// Printf 以 INFO 等級輸出（對應 LOGGER.info）。
func (l *LeveledLogger) Printf(format string, args ...any) {
	l.logf("INFO", format, args...)
}

// Warningf 以 WARNING 等級輸出（對應 LOGGER.warning）。
func (l *LeveledLogger) Warningf(format string, args ...any) {
	l.logf("WARNING", format, args...)
}

// Errorf 以 ERROR 等級輸出（對應 LOGGER.error）。
func (l *LeveledLogger) Errorf(format string, args ...any) {
	l.logf("ERROR", format, args...)
}

// logf 組裝前綴後寫入所有下游 Writer。
func (l *LeveledLogger) logf(level, format string, args ...any) {
	text := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
	if text == "" {
		return
	}
	line := fmt.Sprintf("%s - %s - %s\n", time.Now().Format("2006-01-02 15:04:05"), level, text)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.writers {
		_, _ = io.WriteString(w, line)
	}
}
