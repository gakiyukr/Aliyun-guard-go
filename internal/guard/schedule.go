// Package guard 是核心決策引擎：每日排程、CDT 月結算判斷、每實例開關機決策。
// 本檔對應 Python 版的 normalize_schedule_time / schedule_target / next_schedule_event
// 等排程函式群。排程時刻以 schedule.timezone 指定的時區解讀（預設 Asia/Taipei），
// 不依賴部署機器的系統時區。
package guard

import (
	"fmt"
	"strings"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/xtime"
)

// NormalizeScheduleTime 嚴格驗證 HH:MM 格式並正規化為兩位數，對應 normalize_schedule_time()。
func NormalizeScheduleTime(value, fieldName string) (string, error) {
	text := fmt.Sprintf("%v", value)
	if len(text) != 5 || text[2] != ':' || !isDigits(text[:2]) || !isDigits(text[3:]) {
		return "", fmt.Errorf("%s必须使用 HH:MM 格式，例如 08:30", fieldName)
	}
	hour := int(text[0]-'0')*10 + int(text[1]-'0')
	minute := int(text[3]-'0')*10 + int(text[4]-'0')
	if hour > 23 || minute > 59 {
		return "", fmt.Errorf("%s超出有效范围", fieldName)
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// GetScheduleConfig 取出並驗證單一實例的每日排程設定，對應 get_schedule_config()。
// timezone 驗證失敗時比照 HH:MM 驗證停用排程，避免錯誤設定觸發意外開關機。
func GetScheduleConfig(user map[string]any) map[string]any {
	schedule := config.DeepMerge(config.DefaultSchedule(), config.GetMap(user, "schedule"))
	schedule["enabled"] = toBool(schedule["enabled"])
	if schedule["enabled"].(bool) {
		start, err := NormalizeScheduleTime(fmt.Sprintf("%v", schedule["start_time"]), "开机时间")
		if err != nil {
			schedule["enabled"] = false
		} else {
			schedule["start_time"] = start
		}
		stop, err := NormalizeScheduleTime(fmt.Sprintf("%v", schedule["stop_time"]), "关机时间")
		if err != nil {
			schedule["enabled"] = false
		} else {
			schedule["stop_time"] = stop
		}
	}
	schedule["timezone"] = NormalizeScheduleTimezone(schedule["timezone"])
	if schedule["enabled"].(bool) {
		if _, err := xtime.LoadScheduleLocation(schedule["timezone"].(string)); err != nil {
			schedule["enabled"] = false
		}
	}
	return schedule
}

// NormalizeScheduleTimezone 將 timezone 欄位正規化為去除前後空白的字串。
// JSON null / 缺失視為未設定，回傳預設值 xtime.ScheduleDefaultZone。
func NormalizeScheduleTimezone(value any) string {
	if value == nil {
		return xtime.ScheduleDefaultZone
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" {
		return xtime.ScheduleDefaultZone
	}
	return text
}

// ScheduleLocation 回傳排程設定的時區。呼叫前應先經 GetScheduleConfig 驗證，
// 驗證失敗的防禦性回退為固定 UTC+8（與預設 Asia/Taipei 等效且無需 zoneinfo）。
func ScheduleLocation(schedule map[string]any) *time.Location {
	name, _ := schedule["timezone"].(string)
	loc, err := xtime.LoadScheduleLocation(name)
	if err != nil || loc == nil {
		return xtime.CDT
	}
	return loc
}

// ScheduleTarget 回傳當前時刻排程期望的實例狀態："running" / "stopped" / nil（未啟用）。
// 以「當日分鐘數」純數值比較，支援跨午夜區間（start >= stop），對應 schedule_target()。
// 時刻以排程時區（schedule.timezone，預設 Asia/Taipei）解讀，與部署機器時區無關。
func ScheduleTarget(user map[string]any, now time.Time) any {
	schedule := GetScheduleConfig(user)
	enabled, _ := schedule["enabled"].(bool)
	if !enabled {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	local := now.In(ScheduleLocation(schedule))
	current := local.Hour()*60 + local.Minute()
	start := minutesOfDay(schedule["start_time"].(string))
	stop := minutesOfDay(schedule["stop_time"].(string))
	var running bool
	if start < stop {
		running = current >= start && current < stop
	} else {
		running = current >= start || current < stop
	}
	if running {
		return "running"
	}
	return "stopped"
}

// ComposeScheduleSignature 組合排程指紋字串，ScheduleSignature 與 updateState
// 必須使用同一組合方式，否則每輪都會誤判排程變更。空時區正規化為預設值。
func ComposeScheduleSignature(start, stop, timezone string) string {
	return start + "|" + stop + "|" + NormalizeScheduleTimezone(timezone)
}

// ScheduleSignature 回傳排程設定指紋，用於偵測排程變更，對應 schedule_signature()。
// 時區屬於指紋的一部分：變更時區會觸發一次排程遷移，立即對齊新時區的目標狀態。
func ScheduleSignature(user map[string]any) string {
	schedule := GetScheduleConfig(user)
	enabled, _ := schedule["enabled"].(bool)
	if !enabled {
		return "disabled"
	}
	return ComposeScheduleSignature(
		schedule["start_time"].(string),
		schedule["stop_time"].(string),
		schedule["timezone"].(string),
	)
}

// ScheduleTransition 比對排程與上一輪狀態，回傳應執行的動作 "start"/"stop" 或空字串，
// 對應 schedule_transition()。排程優先於流量規則，且暫停中的實例不動作。
func ScheduleTransition(user map[string]any, previous map[string]any, now time.Time) string {
	if toBool(user["paused"]) {
		return ""
	}
	schedule := GetScheduleConfig(user)
	enabled, _ := schedule["enabled"].(bool)
	if !enabled {
		return ""
	}
	if previous == nil {
		previous = map[string]any{}
	}
	target := ScheduleTarget(user, now)
	sig := ScheduleSignature(user)
	prevSig, _ := previous["schedule_signature"].(string)
	prevTarget, _ := previous["schedule_target"].(string)
	if prevSig != sig || prevTarget != fmt.Sprintf("%v", target) {
		if fmt.Sprintf("%v", target) == "running" {
			return "start"
		}
		return "stop"
	}
	return ""
}

// NextScheduleEvent 回傳下一次排程事件（時間, 動作），對應 next_schedule_event()。
// 今天已過的時刻順延一天，取開/關兩個時刻中較近者；「今天」以排程時區界定。
func NextScheduleEvent(user map[string]any, now time.Time) (time.Time, string, bool) {
	schedule := GetScheduleConfig(user)
	enabled, _ := schedule["enabled"].(bool)
	if !enabled {
		return time.Time{}, "", false
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(ScheduleLocation(schedule))
	var best time.Time
	var bestAction string
	for _, pair := range []struct {
		action string
		value  string
	}{{"start", schedule["start_time"].(string)}, {"stop", schedule["stop_time"].(string)}} {
		h, m := parseHM(pair.value)
		candidate := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if !candidate.After(now) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		if best.IsZero() || candidate.Before(best) {
			best = candidate
			bestAction = pair.action
		}
	}
	return best, bestAction, true
}

// HasDueSchedule 檢查是否有任何實例的排程產生待執行動作（供每分鐘喚醒判斷），
// 對應 has_due_schedule()。
func HasDueSchedule(cfg map[string]any, st map[string]any, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	instances, _ := st["instances"].(map[string]any)
	for _, user := range config.Users(cfg) {
		instanceID := config.GetStr(user, "instance_id")
		prev, _ := instances[instanceID].(map[string]any)
		if ScheduleTransition(user, prev, now) != "" {
			return true
		}
	}
	return false
}

func minutesOfDay(hhmm string) int {
	return int(hhmm[0]-'0')*600 + int(hhmm[1]-'0')*60 + int(hhmm[3]-'0')*10 + int(hhmm[4]-'0')
}

func parseHM(hhmm string) (int, int) {
	return int(hhmm[0]-'0')*10 + int(hhmm[1]-'0'), int(hhmm[3]-'0')*10 + int(hhmm[4]-'0')
}

func toBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// toBool2 寬鬆布林轉換，無法識別時回傳預設值（比照 config.GetBool 語意）。
func toBool2(v any, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "true" || t == "1"
	}
	return def
}
