// 彙總通知的組裝與過濾，對應 Python 版 build_summary / should_notify。
package guard

import (
	"fmt"
	"strings"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/notify"
)

// levelIcon 對應 level_icon() 的等級圖示。
func levelIcon(level string) string {
	switch level {
	case "ok":
		return "✅"
	case "action":
		return "🛠"
	case "warning":
		return "⚠️"
	case "error":
		return "❌"
	case "paused":
		return "⏸"
	default:
		return "•"
	}
}

// ShouldNotify 依 notification_mode 判斷本輪是否發送彙總，對應 should_notify()：
// always：每輪；errors：僅出錯；events：有動作/警告/排程事件/狀態變化時。
// previous 為更新前的 state["instances"]，用於偵測狀態變化。
func ShouldNotify(cfg map[string]any, results []*CheckResult, previous map[string]any) bool {
	mode := config.GetStr(cfg, "notification_mode")
	if mode == "" {
		mode = "always"
	}
	if mode == "always" {
		return true
	}
	if anyError(results) {
		return true
	}
	if mode == "errors" {
		return false
	}
	for _, r := range results {
		if r.Action != "none" || r.Level == "warning" || r.ScheduleEvent != "" {
			return true
		}
	}
	for _, r := range results {
		old, _ := previous[r.InstanceID].(map[string]any)
		if old == nil {
			continue
		}
		if oldStatus, _ := old["status_after"].(string); oldStatus != "" && oldStatus != r.StatusAfter {
			return true
		}
	}
	return false
}

func anyError(results []*CheckResult) bool {
	for _, r := range results {
		if r.Level == "error" {
			return true
		}
	}
	return false
}

// BuildSummary 組裝每輪檢測的彙總文字，對應 build_summary()。
func BuildSummary(results []*CheckResult, startedAt time.Time, dryRun bool) string {
	title := "检测汇总"
	if dryRun {
		title = "检测汇总（演练）"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s\n时间: %s\n", title, startedAt.Format("2006-01-02 15:04:05")))
	for _, r := range results {
		b.WriteString(fmt.Sprintf("\n%s %s\n", levelIcon(r.Level), r.Name))
		if r.TrafficGB != nil {
			b.WriteString(fmt.Sprintf("流量: %.2f/%.2f GB\n", *r.TrafficGB, r.LimitGB))
		} else {
			b.WriteString("流量: 查询失败\n")
		}
		if r.BillAmount != nil {
			b.WriteString(fmt.Sprintf("账单: %s%.2f (%s)\n", r.BillSymbol, *r.BillAmount, r.BillCurrency))
		}
		if r.StatusAfter != "" {
			b.WriteString(fmt.Sprintf("状态: %s\n", r.StatusAfter))
		}
		if r.Message != "" {
			b.WriteString(fmt.Sprintf("结果: %s\n", r.Message))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// notifySummary 發送彙總通知，長訊息自動拆分。
func notifySummary(cfg map[string]any, results []*CheckResult, startedAt time.Time, dryRun bool) error {
	text := BuildSummary(results, startedAt, dryRun)
	var lastErr error
	for _, part := range notify.SplitMessage(text, 3900) {
		if err := notify.SendMessage(cfg, part); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
