// per-instance 日誌，對應 aliyun_guard.py 的 write_instance_log 與
// web_panel.py 的 _write_manual_instance_log：獨立輪轉檔（每日、保留 14 天）、
// 「事件=… | 实例=… | …」欄位格式、AK/SK/Bot Token/連結脫敏。
package guard

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/logging"
)

const instanceLogKeep = 14

var (
	instanceLogMu    sync.Mutex
	instanceLogFiles = map[string]*instanceLogger{}
)

type instanceLogger struct {
	writer *logging.Writer
	line   *logging.PrefixWriter
}

// InstanceLogPath 對應 instance_log_path()：logs/instances/<safe-id>-<hash[:10]>.log。
func InstanceLogPath(paths config.Paths, user map[string]any) string {
	instanceID := strings.TrimSpace(config.GetStr(user, "instance_id"))
	region := strings.TrimSpace(config.GetStr(user, "region"))
	safe := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(instanceID, "-")
	safe = strings.Trim(safe, ".-_")
	if len(safe) > 64 {
		safe = safe[:64]
	}
	if safe == "" {
		safe = "instance"
	}
	digest := sha256.Sum256([]byte(region + "\x00" + instanceID))
	return filepath.Join(logsDir(paths), "instances",
		fmt.Sprintf("%s-%x.log", safe, digest[:5]))
}

func logsDir(paths config.Paths) string {
	if env := strings.TrimSpace(os.Getenv("ALIYUN_GUARD_LOG_DIR")); env != "" {
		return env
	}
	return filepath.Join(filepath.Dir(paths.Config), "logs")
}

// instanceLogWriter 取得（或建立）單一實例的輪轉寫入器。
func instanceLogWriter(paths config.Paths, user map[string]any) (*logging.PrefixWriter, error) {
	path := InstanceLogPath(paths, user)
	instanceLogMu.Lock()
	defer instanceLogMu.Unlock()
	if logger, ok := instanceLogFiles[path]; ok {
		return logger.line, nil
	}
	writer := logging.New(path, instanceLogKeep)
	line := &logging.PrefixWriter{Next: writer, Level: "INFO"}
	instanceLogFiles[path] = &instanceLogger{writer: writer, line: line}
	return line, nil
}

// writeInstanceLogLine 以對應等級寫入一行（error/warning/action/paused）。
func writeInstanceLogLine(paths config.Paths, user map[string]any, level, message string) {
	line, err := instanceLogWriter(paths, user)
	if err != nil {
		Logger.Errorf("[%s] 独立日志写入失败: %v",
			orDefault(config.GetStr(user, "name"), config.GetStr(user, "instance_id"), "未命名"), err)
		return
	}
	_, _ = line.Write([]byte(message))
}

func orDefault(value string, fallbacks ...string) string {
	if value != "" {
		return value
	}
	for _, f := range fallbacks {
		if f != "" {
			return f
		}
	}
	return ""
}

// instanceLogValue 對應 _instance_log_value()：壓平空白、脫敏金鑰/Bot Token/
// 連結，截斷至 limit（0 表不截斷），空值以「-」呈現。
func instanceLogValue(value any, user map[string]any, limits ...int) string {
	limit := 0
	if len(limits) > 0 {
		limit = limits[0]
	}
	text := strings.Join(strings.Fields(
		strings.ReplaceAll(toStringValue(value), "\r", " ")), " ")
	for _, secret := range []any{user["ak"], user["sk"]} {
		if s := toStringValue(secret); s != "" {
			text = strings.ReplaceAll(text, s, "***")
		}
	}
	text = linkPattern.ReplaceAllString(text, "[链接已隐藏]")
	text = botTokenPattern.ReplaceAllString(text, "[Bot Token 已隐藏]")
	if limit > 0 && len([]rune(text)) > limit {
		text = string([]rune(text)[:limit])
	}
	if text == "" {
		return "-"
	}
	return text
}

var (
	linkPattern = regexp.MustCompile(
		`(?i)\b(?:https?|socks5h?|vless|vmess|ss|trojan|hysteria2|hy2|tuic|anytls)://[^\s；，,]+`)
	botTokenPattern = regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{20,}\b`)
)

func toStringValue(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("%g", s)
	case bool:
		if s {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", s)
	}
}

// InstanceLogEntry 為 write_instance_log 的輸入欄位（map 形式與 Python dict 對應）。
type InstanceLogEntry struct {
	Name            string
	InstanceID      string
	TrafficGB       *float64
	LimitGB         float64
	StatusBefore    string
	StatusAfter     string
	BillingEnabled  bool
	BillingChecked  bool // false → 账单显示「未查询」
	BillAmount      *float64
	BillCurrency    string
	BillSymbol      string
	BillError       string
	Action          string
	ActionPerformed bool
	Level           string
	Message         string
	Errors          []string
}

// WriteInstanceLog 對應 write_instance_log()：實例啟用獨立日誌時寫入一行
// 「事件=… | 实例=… | 结果=… | 流量=… | ECS=… | 账单=… | 动作=… | 已执行=… |
// 演练=… | 说明=… | 错误=…」。
func WriteInstanceLog(
	paths config.Paths, user map[string]any, entry InstanceLogEntry,
	dryRun bool, event string,
) bool {
	if !config.GetBool(user, "instance_log_enabled", false) {
		return false
	}
	trafficText := "无数据"
	switch {
	case entry.TrafficGB != nil && entry.LimitGB > 0:
		trafficText = fmt.Sprintf("%.2f/%.2f GB", *entry.TrafficGB, entry.LimitGB)
	case entry.TrafficGB != nil:
		trafficText = fmt.Sprintf("%.2f GB", *entry.TrafficGB)
	}
	before := orDefault(entry.StatusBefore, "Unknown")
	after := orDefault(entry.StatusAfter, before)
	statusText := before
	if before != after {
		statusText = before + "->" + after
	}
	billText := "无数据"
	switch {
	case !entry.BillingChecked:
		billText = "未查询"
	case !entry.BillingEnabled:
		billText = "已关闭"
	case entry.BillError != "":
		billText = "失败: " + instanceLogValue(entry.BillError, user)
	case entry.BillAmount != nil:
		billText = strings.TrimSpace(fmt.Sprintf("%s%.2f %s",
			entry.BillSymbol, *entry.BillAmount, entry.BillCurrency))
	}
	errorsText := "无"
	if parts := nonEmptyList(entry.Errors); len(parts) > 0 {
		for i, part := range parts {
			parts[i] = instanceLogValue(part, user)
		}
		errorsText = strings.Join(parts, "；")
	}
	message := strings.Join([]string{
		"事件=" + instanceLogValue(event, user, 80),
		"实例=" + instanceLogValue(orDefault(entry.Name,
			config.GetStr(user, "name"), config.GetStr(user, "instance_id")), user, 120) +
			" (" + instanceLogValue(orDefault(entry.InstanceID,
			config.GetStr(user, "instance_id")), user, 120) + ")",
		"结果=" + instanceLogValue(orDefault(entry.Level, "unknown"), user, 40),
		"流量=" + trafficText,
		"ECS=" + instanceLogValue(statusText, user, 120),
		"账单=" + billText,
		"动作=" + instanceLogValue(orDefault(entry.Action, "none"), user, 80),
		"已执行=" + boolText(entry.ActionPerformed),
		"演练=" + boolText(dryRun),
		"说明=" + instanceLogValue(entry.Message, user, 500),
		"错误=" + errorsText,
	}, " | ")
	writeInstanceLogLine(paths, user, entry.Level, message)
	return true
}

// CloseInstanceLogs 關閉並清空所有已快取的實例日誌寫入器（關機與測試用）。
func CloseInstanceLogs() {
	instanceLogMu.Lock()
	defer instanceLogMu.Unlock()
	for path, logger := range instanceLogFiles {
		_ = logger.writer.Close()
		delete(instanceLogFiles, path)
	}
}

func nonEmptyList(values []string) []string {
	out := []string{}
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func boolText(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

// CheckResultEntry 將 CheckResult 轉為 InstanceLogEntry（週期檢測路徑）。
func CheckResultEntry(r *CheckResult) InstanceLogEntry {
	amount := r.BillAmount
	return InstanceLogEntry{
		Name: r.Name, InstanceID: r.InstanceID,
		TrafficGB: r.TrafficGB, LimitGB: r.LimitGB,
		StatusBefore: r.StatusBefore, StatusAfter: r.StatusAfter,
		BillingEnabled: r.BillingEnabled, BillingChecked: true,
		BillAmount: amount, BillCurrency: r.BillCurrency, BillSymbol: r.BillSymbol,
		BillError: r.BillError,
		Action:    r.Action, ActionPerformed: r.ActionPerformed,
		Level: r.Level, Message: r.Message, Errors: r.Errors,
	}
}

// ManualLogEntry 對應 web_panel._write_manual_instance_log 的欄位組合
// （event 由呼叫端組合 source + 「手动」+ 動詞）。
func ManualLogEntry(user map[string]any, action, before, after string,
	traffic *float64, performed bool, message, level string, errors []string,
) InstanceLogEntry {
	return InstanceLogEntry{
		Name: config.GetStr(user, "name"), InstanceID: config.GetStr(user, "instance_id"),
		TrafficGB: traffic, LimitGB: config.GetFloat(user, "traffic_limit_gb", 0),
		StatusBefore: before, StatusAfter: afterOr(before, after),
		BillingEnabled: config.GetBool(config.BillingConfig(user), "enabled", true),
		BillingChecked: false,
		Action:         "manual_" + action, ActionPerformed: performed,
		Level: level, Message: message, Errors: errors,
	}
}

var _ io.Writer = (*logging.Writer)(nil)
