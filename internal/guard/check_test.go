// 決策引擎測試：dry-run 決策表與實際開關機的訊息措辭（經 stub 鉤子）。
package guard

import (
	"errors"
	"testing"
	"time"
)

// stubAll 以 stub 替換全部阿里雲呼叫鉤子，回傳還原函式。
func stubAll(status string, statusErr error) func() {
	origStatus, origStart, origStop, origBill, origTraffic :=
		queryStatusFn, startFn, stopFn, billQueryFn, aliyunListTraffic
	queryStatusFn = func(ak, sk, region, id string) (string, error) { return status, statusErr }
	startFn = func(ak, sk, region, id string) error { return nil }
	stopFn = func(ak, sk, region, id string) error { return nil }
	billQueryFn = func(ak, sk, endpoint, instanceID, cycle string) (float64, string, error) {
		return 0.01, "USD", nil
	}
	aliyunListTraffic = func(ak, sk string) (float64, error) { return 0.5, nil }
	return func() {
		queryStatusFn, startFn, stopFn, billQueryFn, aliyunListTraffic =
			origStatus, origStart, origStop, origBill, origTraffic
	}
}

func baseUser() map[string]any {
	return map[string]any{
		"name": "A", "instance_id": "i-1", "region": "cn-x",
		"ak": "a", "sk": "s", "traffic_limit_gb": 100.0,
	}
}

func TestCheckOneDryRunStart(t *testing.T) {
	restore := stubAll("Stopped", nil)
	defer restore()
	res := CheckOne(baseUser(), map[string]any{}, true, time.Time{}, "",
		map[string]cachedTraffic{}, nil, nil, false)
	if res.Action != "start" || res.Level != "action" || res.Message != "演练：应启动实例" {
		t.Fatalf("低流量+停機 演練應為 start，got %+v", res)
	}
}

func TestCheckOneDryRunStop(t *testing.T) {
	restore := stubAll("Running", nil)
	defer restore()
	aliyunListTraffic = func(ak, sk string) (float64, error) { return 200, nil }
	res := CheckOne(baseUser(), map[string]any{}, true, time.Time{}, "",
		map[string]cachedTraffic{}, nil, nil, false)
	if res.Action != "stop" || res.Message != "演练：流量达到阈值，应停止实例" {
		t.Fatalf("超流量+運行 演練應為 stop，got %+v", res)
	}
}

func TestCheckOneScheduleShutdownPriority(t *testing.T) {
	// 排程關機時段：即使 CDT 查詢失敗也應執行關機（不阻擋止損）
	restore := stubAll("Running", nil)
	defer restore()
	aliyunListTraffic = func(ak, sk string) (float64, error) { return 0, errors.New("cdt down") }
	user := baseUser()
	user["schedule"] = map[string]any{"enabled": true, "start_time": "22:00", "stop_time": "06:00"}
	// 關機時段為 06:00~22:00（start>=stop 為跨午夜運行區間），12:00 落在關機時段
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	res := CheckOne(user, map[string]any{}, true, now, "", map[string]cachedTraffic{}, nil, nil, false)
	if res.Action != "schedule_stop" || res.Message != "演练：当前处于计划关机时段，应停止实例" {
		t.Fatalf("排程關機時段演練應為 schedule_stop，got %+v", res)
	}
}

func TestCheckOneCoreQueryFailureBlocksAction(t *testing.T) {
	restore := stubAll("", errors.New("ecs down"))
	defer restore()
	aliyunListTraffic = func(ak, sk string) (float64, error) { return 0, errors.New("cdt down") }
	res := CheckOne(baseUser(), map[string]any{}, false, time.Time{}, "",
		map[string]cachedTraffic{}, nil, nil, false)
	if res.Level != "error" || res.Message != "CDT 或 ECS 核心查询失败，本轮未执行开关机" {
		t.Fatalf("核心查詢失敗應不動作，got %+v", res)
	}
}

func TestCheckOneStopWithConfirm(t *testing.T) {
	// 非演練：超流量 + Running → 關機，狀態輪詢第二次查詢即確認 Stopped
	restore := stubAll("Running", nil)
	defer restore()
	calls := 0
	queryStatusFn = func(ak, sk, region, id string) (string, error) {
		calls++
		if calls == 1 {
			return "Running", nil
		}
		return "Stopped", nil
	}
	aliyunListTraffic = func(ak, sk string) (float64, error) { return 200, nil }
	res := CheckOne(baseUser(), map[string]any{}, false, time.Time{}, "",
		map[string]cachedTraffic{}, nil, nil, false)
	if !res.ActionPerformed || res.Level != "action" ||
		res.Message != "流量达到阈值，已停止并确认实例关机" || res.StatusAfter != "Stopped" {
		t.Fatalf("關機確認路徑訊息不符，got %+v", res)
	}
}

func TestCheckOnePollWaitMessage(t *testing.T) {
	// 非演練：低流量 + Stopped → 保活啟動，輪詢始終回 Starting → 等待措辭
	restore := stubAll("Stopped", nil)
	defer restore()
	calls := 0
	queryStatusFn = func(ak, sk, region, id string) (string, error) {
		calls++
		if calls == 1 {
			return "Stopped", nil
		}
		return "Starting", nil
	}
	aliyunListTraffic = func(ak, sk string) (float64, error) { return 0.5, nil }
	// start_wait_seconds=0 → waitForStatus 立即逾時回 "Starting"
	cfg := map[string]any{"start_wait_seconds": 0, "start_poll_seconds": 1}
	res := CheckOne(baseUser(), cfg, false, time.Time{}, "",
		map[string]cachedTraffic{}, nil, nil, false)
	if res.Level != "warning" || res.Message != "已提交保活启动请求，等待 0 秒后状态为 Starting" {
		t.Fatalf("等待措辭不符，got %+v", res)
	}
}
