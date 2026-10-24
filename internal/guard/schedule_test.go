// 排程邏輯測試：HH:MM 驗證、跨午夜區間、下次事件、排程遷移偵測。
package guard

import (
	"testing"
	"time"
)

func TestNormalizeScheduleTime(t *testing.T) {
	got, err := NormalizeScheduleTime("8:5", "时间") // 非兩位數格式
	if err == nil {
		t.Fatal("寬鬆格式應被拒絕")
	}
	if _, err := NormalizeScheduleTime("25:00", "时间"); err == nil {
		t.Fatal("小時超範圍應被拒絕")
	}
	if _, err := NormalizeScheduleTime("08:60", "时间"); err == nil {
		t.Fatal("分鐘超範圍應被拒絕")
	}
	got, err = NormalizeScheduleTime("08:05", "时间")
	if err != nil || got != "08:05" {
		t.Fatalf("正常格式應通過: %s %v", got, err)
	}
}

func TestScheduleTarget(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00"},
	}
	loc := time.FixedZone("test", 8*3600)
	// 區間內 → running
	if got := ScheduleTarget(user, time.Date(2026, 9, 10, 12, 0, 0, 0, loc)); got != "running" {
		t.Fatalf("區間內應為 running，got %v", got)
	}
	// 區間外 → stopped
	if got := ScheduleTarget(user, time.Date(2026, 9, 10, 5, 0, 0, 0, loc)); got != "stopped" {
		t.Fatalf("區間外應為 stopped，got %v", got)
	}
}

func TestScheduleTargetCrossMidnight(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "22:00", "stop_time": "06:00"},
	}
	loc := time.FixedZone("test", 8*3600)
	for _, h := range []int{23, 0, 3, 5} {
		if got := ScheduleTarget(user, time.Date(2026, 9, 10, h, 30, 0, 0, loc)); got != "running" {
			t.Fatalf("跨午夜區間 %d:30 應為 running，got %v", h, got)
		}
	}
	if got := ScheduleTarget(user, time.Date(2026, 9, 10, 12, 0, 0, 0, loc)); got != "stopped" {
		t.Fatalf("白天應為 stopped，got %v", got)
	}
}

func TestNextScheduleEvent(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00"},
	}
	loc := time.FixedZone("test", 8*3600)
	// 12:00 → 下次事件是今天 23:00 的 stop
	gotAt, gotAction, ok := NextScheduleEvent(user, time.Date(2026, 9, 10, 12, 0, 0, 0, loc))
	if !ok || gotAction != "stop" || gotAt.Hour() != 23 {
		t.Fatalf("下次事件應為今日 23:00 stop，got %v %v", gotAt, gotAction)
	}
	// 23:30 → 下次事件是明天 08:00 的 start
	gotAt, gotAction, _ = NextScheduleEvent(user, time.Date(2026, 9, 10, 23, 30, 0, 0, loc))
	if gotAction != "start" || gotAt.Day() != 11 || gotAt.Hour() != 8 {
		t.Fatalf("下次事件應為明日 08:00 start，got %v %v", gotAt, gotAction)
	}
}

func TestScheduleTransitionDetectsSignatureChange(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00"},
	}
	loc := time.FixedZone("test", 8*3600)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	// 前值指紋與目標一致 → 無動作
	prev := map[string]any{"schedule_signature": "08:00|23:00|Asia/Taipei", "schedule_target": "running"}
	if got := ScheduleTransition(user, prev, now); got != "" {
		t.Fatalf("指紋一致不應有動作，got %q", got)
	}
	// 排程變更 → 觸發動作
	user["schedule"].(map[string]any)["start_time"] = "09:00"
	if got := ScheduleTransition(user, prev, now); got == "" {
		t.Fatal("指紋變更應觸發動作")
	}
}

func TestScheduleTargetUsesScheduleTimezone(t *testing.T) {
	// 排程時刻以排程時區解讀，與傳入 now 的所屬時區（部署機器）無關
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00", "timezone": "UTC+8"},
	}
	// UTC 02:00 = UTC+8 10:00 → 區間內 running
	if got := ScheduleTarget(user, time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC)); got != "running" {
		t.Fatalf("UTC 02:00 應為 UTC+8 10:00 running，got %v", got)
	}
	// UTC 16:00 = UTC+8 次日 00:00 → 區間外 stopped
	if got := ScheduleTarget(user, time.Date(2026, 9, 10, 16, 0, 0, 0, time.UTC)); got != "stopped" {
		t.Fatalf("UTC 16:00 應為 UTC+8 次日 00:00 stopped，got %v", got)
	}
}

func TestScheduleDefaultTimezoneIsTaipei(t *testing.T) {
	// 未設定 timezone → 預設 Asia/Taipei（UTC+8），而非系統本地時區
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00"},
	}
	schedule := GetScheduleConfig(user)
	if schedule["timezone"] != "Asia/Taipei" {
		t.Fatalf("預設時區應為 Asia/Taipei，got %v", schedule["timezone"])
	}
	// 本機為 UTC（即便如此）：UTC 02:00 = 台北 10:00 → running
	if got := ScheduleTarget(user, time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC)); got != "running" {
		t.Fatalf("預設時區下 UTC 02:00 應為 running，got %v", got)
	}
}

func TestScheduleNextEventInScheduleTimezone(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00", "timezone": "UTC+8"},
	}
	// UTC 2026-09-10 23:30 = UTC+8 2026-09-11 07:30 → 下次事件為（UTC+8）今日 08:00 開機
	gotAt, gotAction, ok := NextScheduleEvent(user, time.Date(2026, 9, 10, 23, 30, 0, 0, time.UTC))
	if !ok || gotAction != "start" || gotAt.Hour() != 8 || gotAt.Day() != 11 {
		t.Fatalf("下次事件應為 UTC+8 09-11 08:00 start，got %v %v %v", gotAt, gotAction, ok)
	}
}

func TestScheduleInvalidTimezoneDisables(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00", "timezone": "No/SuchZone"},
	}
	schedule := GetScheduleConfig(user)
	if schedule["enabled"] != false {
		t.Fatal("非法時區應停用排程")
	}
	if got := ScheduleTarget(user, time.Now()); got != nil {
		t.Fatalf("排程停用時目標應為 nil，got %v", got)
	}
}

func TestScheduleSignatureIncludesTimezone(t *testing.T) {
	user := map[string]any{
		"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00", "timezone": "UTC+8"},
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	prev := map[string]any{"schedule_signature": "08:00|23:00|Asia/Taipei", "schedule_target": "running"}
	// 僅變更時區也應觸發排程遷移，立即對齊新時區的目標狀態
	if got := ScheduleTransition(user, prev, now); got == "" {
		t.Fatal("時區變更應觸發動作")
	}
}

func TestNormalizeScheduleTimezone(t *testing.T) {
	if got := NormalizeScheduleTimezone(nil); got != "Asia/Taipei" {
		t.Fatalf("nil 應回傳預設時區，got %q", got)
	}
	if got := NormalizeScheduleTimezone(""); got != "Asia/Taipei" {
		t.Fatalf("空字串應回傳預設時區，got %q", got)
	}
	if got := NormalizeScheduleTimezone(" UTC+8 "); got != "UTC+8" {
		t.Fatalf("應去除空白，got %q", got)
	}
}

func TestComposeScheduleSignatureNormalizesTimezone(t *testing.T) {
	if got := ComposeScheduleSignature("08:00", "23:00", ""); got != "08:00|23:00|Asia/Taipei" {
		t.Fatalf("空時區應正規化為預設值，got %q", got)
	}
}
