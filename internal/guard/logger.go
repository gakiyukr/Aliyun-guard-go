// 分級日誌介面，對應 Python 版 LOGGER 的 info/warning/error 三級語意。
// 上游以 LOGGER.error/warning 區分日誌等級；Go 版以 LevelLogger 介面
// 達成同一效果，主程式注入 logging.LeveledLogger 實作。
package guard

import "log"

// LevelLogger 提供 INFO（Printf）/WARNING（Warningf）/ERROR（Errorf）三級輸出。
type LevelLogger interface {
	Printf(format string, args ...any)
	Warningf(format string, args ...any)
	Errorf(format string, args ...any)
}

// stdLevelLogger 以標準庫 log.Logger 作為未注入時的退路（三級同輸出）。
type stdLevelLogger struct{ l *log.Logger }

func (s stdLevelLogger) Printf(format string, args ...any)   { s.l.Printf(format, args...) }
func (s stdLevelLogger) Warningf(format string, args ...any) { s.l.Printf(format, args...) }
func (s stdLevelLogger) Errorf(format string, args ...any)   { s.l.Printf(format, args...) }

// Logger 由主程式注入（logging.LeveledLogger），避免循環依賴；
// 未注入時退回標準庫 Logger（等級退化為統一輸出）。
var Logger LevelLogger = stdLevelLogger{log.Default()}
