// 調度層測試：月結算三級 fallback、is_due、update_state 的 dry-run 語意。
package guard

import (
	"testing"
	"time"

	"aliyun-guard-go/internal/xtime"
)

func TestMonthlyResetDueFallbacks(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	// 一級：cdt_month_checked 字串
	if !MonthlyResetDue(map[string]any{"cdt_month_checked": "2026-08"}, now) {
		t.Fatal("上月應判斷到期")
	}
	if MonthlyResetDue(map[string]any{"cdt_month_checked": "2026-09"}, now) {
		t.Fatal("當月不應到期")
	}
	// 二級：ISO 時間戳
	st := map[string]any{"last_cycle_finished_at": "2026-08-31T20:00:00+08:00"}
	if !MonthlyResetDue(st, now) {
		t.Fatal("ISO 上月應到期")
	}
	// 三級：epoch（2026-09-01 00:00 UTC+8 = 2026-08-31 16:00 UTC）
	epoch := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC).Unix()
	if MonthlyResetDue(map[string]any{"last_cycle_epoch": float64(epoch)}, now) {
		t.Fatal("epoch 當月不應到期")
	}
	// 全空 → false（新安裝不做補檢）
	if MonthlyResetDue(map[string]any{}, now) {
		t.Fatal("空狀態不應到期")
	}
}

func TestIsDue(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	cfg := map[string]any{"interval_seconds": 300}
	// 無任何時間戳 → true
	if !IsDue(cfg, map[string]any{}, now) {
		t.Fatal("空狀態應立即到期")
	}
	// epoch 剛跑過 → false
	st := map[string]any{"last_cycle_epoch": float64(now.Add(-60 * time.Second).Unix())}
	if IsDue(cfg, st, now) {
		t.Fatal("60 秒前剛跑過不應到期")
	}
	// ISO fallback 超過間隔 → true
	st = map[string]any{"last_cycle_epoch": float64(0),
		"last_cycle_finished_at": now.Add(-10 * time.Minute).Format(xtime.ISOFormat)}
	if !IsDue(cfg, st, now) {
		t.Fatal("超過間隔應到期")
	}
}

func TestSchedulerWaitBounds(t *testing.T) {
	cfg := map[string]any{"interval_seconds": 300}
	st := map[string]any{"last_cycle_epoch": float64(time.Now().Add(-10 * time.Second).Unix())}
	wait := SchedulerWait(cfg, st)
	if wait < time.Second || wait > 61*time.Second {
		t.Fatalf("等待時間應在 1~61 秒內，got %v", wait)
	}
}

func TestUpdateStateDryRunSemantics(t *testing.T) {
	st := map[string]any{
		"cycle_count": float64(1),
		"instances": map[string]any{
			"i-1": map[string]any{"schedule_signature": "08:00|23:00|Asia/Taipei", "schedule_target": "running"},
		},
	}
	results := []*CheckResult{{
		Name: "A", InstanceID: "i-1", Level: "ok", Action: "none",
		ScheduleEnabled: true, ScheduleStartTime: "08:00", ScheduleStopTime: "23:00",
		ScheduleTarget: "running", StatusAfter: "Running", TrafficGB: ptrFloat(1.5),
	}}
	updateState(st, results, time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC), time.Second, "summary", nil, true)

	// dry-run 輪：不寫 epoch / cdt_month_checked / history
	if _, exists := st["last_cycle_epoch"]; exists {
		t.Fatal("dry-run 不應寫 last_cycle_epoch")
	}
	if _, exists := st["cdt_month_checked"]; exists {
		t.Fatal("dry-run 不應寫 cdt_month_checked")
	}
	if _, exists := st["history"]; exists {
		t.Fatal("dry-run 不應寫 history")
	}
	// 指紋沿用前值
	inst := st["instances"].(map[string]any)["i-1"].(map[string]any)
	if inst["schedule_signature"] != "08:00|23:00|Asia/Taipei" {
		t.Fatalf("dry-run 應沿用指紋，got %v", inst["schedule_signature"])
	}
	// cycle_count 可能為 int 或 float64（JSON 相容層）
	cc := 0
	switch v := st["cycle_count"].(type) {
	case int:
		cc = v
	case float64:
		cc = int(v)
	}
	if cc != 2 {
		t.Fatalf("cycle_count 應遞增，got %v", st["cycle_count"])
	}

	// 非 dry-run 輪：寫入並更新指紋
	results[0].ScheduleStartTime = "09:00"
	updateState(st, results, time.Now(), time.Second, "summary", nil, false)
	if _, exists := st["last_cycle_epoch"]; !exists {
		t.Fatal("非 dry-run 應寫 last_cycle_epoch")
	}
	inst = st["instances"].(map[string]any)["i-1"].(map[string]any)
	if inst["schedule_signature"] != "09:00|23:00|Asia/Taipei" {
		t.Fatalf("非 dry-run 應更新指紋，got %v", inst["schedule_signature"])
	}
}

func TestShouldNotifyModes(t *testing.T) {
	cfg := map[string]any{"notification_mode": "always"}
	ok := []*CheckResult{{Level: "ok", Action: "none", InstanceID: "i-1", StatusAfter: "Running"}}
	if !ShouldNotify(cfg, ok, map[string]any{}) {
		t.Fatal("always 模式應通知")
	}
	cfg["notification_mode"] = "errors"
	if ShouldNotify(cfg, ok, map[string]any{}) {
		t.Fatal("errors 模式無錯誤不應通知")
	}
	if !ShouldNotify(cfg, []*CheckResult{{Level: "error", Action: "none", InstanceID: "i-1"}}, map[string]any{}) {
		t.Fatal("有錯誤必須通知")
	}
	cfg["notification_mode"] = "events"
	if ShouldNotify(cfg, ok, map[string]any{}) {
		t.Fatal("events 模式無變化不應通知")
	}
	// 狀態變化 → 通知
	prev := map[string]any{"i-1": map[string]any{"status_after": "Stopped"}}
	if !ShouldNotify(cfg, ok, prev) {
		t.Fatal("狀態變化應通知")
	}
	// 動作 → 通知
	if !ShouldNotify(cfg, []*CheckResult{{Level: "ok", Action: "stop", InstanceID: "i-1"}}, map[string]any{}) {
		t.Fatal("有動作應通知")
	}
}

func ptrFloat(f float64) *float64 { return &f }
