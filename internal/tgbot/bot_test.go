// Telegram Bot 控制測試：實例解析、管理員解析、面板文字與排程輸入流程。
package tgbot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/state"
)

func testUsersCfg() map[string]any {
	return map[string]any{"users": []any{
		map[string]any{"name": "HK-CDT", "instance_id": "i-hk", "region": "cn-hk",
			"ak": "a", "sk": "s", "traffic_limit_gb": 100.0,
			"schedule": map[string]any{"enabled": true, "start_time": "08:00", "stop_time": "23:00"}},
		map[string]any{"name": "KR-CDT", "instance_id": "i-kr", "region": "cn-kr",
			"ak": "a", "sk": "s", "traffic_limit_gb": 50.0},
	}}
}

func TestResolveInstance(t *testing.T) {
	users := config.Users(testUsersCfg())
	// 序號（1 起）
	if idx, user := resolveInstance(users, "2"); idx != 1 || config.GetStr(user, "instance_id") != "i-kr" {
		t.Fatalf("序號 2 應解析到 i-kr，got %d", idx)
	}
	// 精確 ID / 名稱
	if idx, _ := resolveInstance(users, "i-hk"); idx != 0 {
		t.Fatalf("ID 應解析到第 0 個，got %d", idx)
	}
	if idx, _ := resolveInstance(users, "HK-CDT"); idx != 0 {
		t.Fatalf("名稱應解析到第 0 個，got %d", idx)
	}
	// 無效輸入
	if _, user := resolveInstance(users, "nope"); user != nil {
		t.Fatal("未知名稱不應解析成功")
	}
	if _, user := resolveInstance(users, ""); user != nil {
		t.Fatal("空選擇器不應解析成功")
	}
}

func TestControlAdminIDs(t *testing.T) {
	// control_admin_ids 優先
	admins := controlAdminIDs(map[string]any{
		"control_admin_ids": []any{111.0, 222.0}, "chat_id": 333.0})
	if !admins[111] || !admins[222] || admins[333] {
		t.Fatalf("應只含 control_admin_ids: %v", admins)
	}
	// 回退正數 chat_id
	admins = controlAdminIDs(map[string]any{"chat_id": -444.0})
	if len(admins) != 0 {
		t.Fatalf("負數 chat_id 不應成為管理員: %v", admins)
	}
	admins = controlAdminIDs(map[string]any{"chat_id": 555.0})
	if !admins[555] {
		t.Fatalf("正數 chat_id 应回退為管理員: %v", admins)
	}
}

func TestBuildStatusAndInstancesText(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{Config: filepath.Join(dir, "config.json"), State: filepath.Join(dir, "state.json")}
	if err := config.Save(paths.Config, testUsersCfg()); err != nil {
		t.Fatal(err)
	}
	st := map[string]any{
		"last_cycle_finished_at": "2026-09-10T12:00:00+08:00",
		"last_cycle_ok":          true,
		"cycle_count":            7,
		"instances": map[string]any{
			"i-hk": map[string]any{"status_after": "Running", "traffic_gb": 12.5},
		},
	}
	if err := state.Save(paths.State, st); err != nil {
		t.Fatal(err)
	}
	status := buildStatusText(paths)
	for _, want := range []string{"Aliyun Guard 状态", "检测结果: 正常", "检测次数: 7", "监控实例: 2 个"} {
		if !strings.Contains(status, want) {
			t.Fatalf("狀態文字缺 %q: %s", want, status)
		}
	}
	instances := buildInstancesText(paths)
	for _, want := range []string{"#1 HK-CDT", "状态: Running · 监控中", "流量: 12.50 GB / 100.00 GB", "ID: i-kr"} {
		if !strings.Contains(instances, want) {
			t.Fatalf("實例文字缺 %q: %s", want, instances)
		}
	}
}

func TestScheduleInputSaveAndCancel(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{Config: filepath.Join(dir, "config.json"), State: filepath.Join(dir, "state.json")}
	if err := config.Save(paths.Config, testUsersCfg()); err != nil {
		t.Fatal(err)
	}
	s := &service{
		paths:          paths,
		pending:        map[string]pendingItem{},
		scheduleInputs: map[int64]scheduleInput{},
	}
	s.scheduleInputs[42] = scheduleInput{
		ChatID: 42, MessageID: 1, InstanceID: "i-hk",
		Expires: time.Now().Add(time.Minute),
	}
	cfg := mustLoadConfig(paths)

	// 非本 chat 的輸入不攔截
	if s.handleScheduleInput(cfg, 42, 999, "09:00 22:00") {
		t.Fatal("chat 不符不應攔截")
	}
	// 過期輸入
	s.scheduleInputs[42] = scheduleInput{ChatID: 42, MessageID: 1, InstanceID: "i-hk", Expires: time.Now().Add(-time.Minute)}
	if !s.handleScheduleInput(cfg, 42, 42, "09:00 22:00") {
		t.Fatal("過期輸入應被攔截並提示")
	}
	// 格式錯誤
	s.scheduleInputs[42] = scheduleInput{ChatID: 42, MessageID: 1, InstanceID: "i-hk", Expires: time.Now().Add(time.Minute)}
	if !s.handleScheduleInput(cfg, 42, 42, "9:00 22:00") {
		t.Fatal("格式錯誤應被攔截")
	}
	// 正常保存
	if !s.handleScheduleInput(cfg, 42, 42, "09:00 22:00") {
		t.Fatal("正常輸入應被攔截並保存")
	}
	if _, exists := s.scheduleInputs[42]; exists {
		t.Fatal("保存後應清除待輸入狀態")
	}
	cfg = mustLoadConfig(paths)
	user := config.Users(cfg)[0]
	schedule := user["schedule"].(map[string]any)
	if schedule["start_time"] != "09:00" || schedule["stop_time"] != "22:00" || schedule["enabled"] != true {
		t.Fatalf("排程應更新為 09:00-22:00 且保持啟用，got %v", schedule)
	}
	// /cancel 退出
	s.scheduleInputs[42] = scheduleInput{ChatID: 42, MessageID: 1, InstanceID: "i-hk", Expires: time.Now().Add(time.Minute)}
	if !s.handleScheduleInput(cfg, 42, 42, "/cancel") {
		t.Fatal("/cancel 應被攔截")
	}
	if _, exists := s.scheduleInputs[42]; exists {
		t.Fatal("/cancel 後應清除待輸入狀態")
	}
}

func TestTokenFingerprintStable(t *testing.T) {
	a := tokenFingerprint("abc")
	b := tokenFingerprint("abc")
	c := tokenFingerprint("abd")
	if a != b || a == c || len(a) != 24 {
		t.Fatalf("指紋應為 24 字元且穩定: %s %s %s", a, b, c)
	}
}

func TestControlStateOffsetPersistence(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{Config: filepath.Join(dir, "config.json")}
	s := &service{paths: paths, fingerprint: tokenFingerprint("tok")}
	if s.loadOffset() != nil {
		t.Fatal("無狀態檔不應有 offset")
	}
	offset := int64(12345)
	s.offset = &offset
	s.saveOffset()
	info, err := os.Stat(s.controlStatePath())
	if err != nil {
		t.Fatalf("狀態檔應存在: %v", err)
	}
	if runtime.GOOS != "windows" { // Windows 不套用 POSIX 權限，部署目標為 Linux
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("狀態檔權限應為 600，got %v", info.Mode().Perm())
		}
	}
	// 指紋不符 → 視為無效
	s2 := &service{paths: paths, fingerprint: tokenFingerprint("other")}
	if s2.loadOffset() != nil {
		t.Fatal("指紋不符不應載入 offset")
	}
	if got := s.loadOffset(); got == nil || *got != 12345 {
		t.Fatalf("應載入 offset 12345，got %v", got)
	}
}
