// 本檔對應 Python 版 check_one()：單一實例的完整檢測與開關機決策。
// 決策優先級與 Python 版一致：
//  1. 暫停中 → 不動作
//  2. 排程關機時段 → 只依賴可讀的 ECS 狀態，直接關機（CDT/BSS 失敗不阻擋，避免實例持續扣費）
//  3. CDT/ECS 核心查詢失敗 → 本輪不動作
//  4. 流量 < 閾值 → 開機（保活）；流量 >= 閾值 → 關機（止損）
//
// 為便於單元測試，阿里雲呼叫皆經過 package 級函式變數（可在測試中替換）。
package guard

import (
	"fmt"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
)

// 阿里雲呼叫鉤子：測試中可替換為 stub。
var (
	queryStatusFn = aliyun.QueryInstanceStatus
	startFn       = aliyun.StartInstance
	stopFn        = aliyun.StopInstance
)

// CheckResult 對應 Python 版 check_one 回傳的 result dict（欄位名與 JSON 相容）。
type CheckResult struct {
	Name              string   `json:"name"`
	InstanceID        string   `json:"instance_id"`
	TrafficGB         *float64 `json:"traffic_gb"`
	LimitGB           float64  `json:"limit_gb"`
	StatusBefore      string   `json:"status_before"`
	StatusAfter       string   `json:"status_after"`
	BillingEnabled    bool     `json:"billing_enabled"`
	BillAmount        *float64 `json:"bill_amount"`
	BillCurrency      string   `json:"bill_currency"`
	BillSymbol        string   `json:"bill_symbol"`
	BillError         string   `json:"bill_error"`
	BillCheckedAt     string   `json:"bill_checked_at"`
	BillFromCache     bool     `json:"bill_from_cache"`
	Action            string   `json:"action"`
	ActionPerformed   bool     `json:"action_performed"`
	Level             string   `json:"level"`
	Message           string   `json:"message"`
	Errors            []string `json:"errors"`
	Paused            bool     `json:"paused"`
	ScheduleEnabled   bool     `json:"schedule_enabled"`
	ScheduleStartTime string   `json:"schedule_start_time"`
	ScheduleStopTime  string   `json:"schedule_stop_time"`
	ScheduleTimezone  string   `json:"schedule_timezone"`
	ScheduleTarget    any      `json:"schedule_target"`
	ScheduleEvent     string   `json:"schedule_event"`
}

// CheckOne 執行單一實例檢測。
//   - dryRun：只判斷不實際開關機
//   - cdtCache：單輪內帳號級 CDT 流量快取（可 nil）
//   - ecsCache：本輪批量 ECS 狀態查詢結果（可 nil，nil 時單查）
//   - billingState：state.json 內容（帳單快取寫入處，可 nil 則不快取）
//   - forceBillingRefresh：CDT 月結算補檢時強制刷新帳單
func CheckOne(
	user map[string]any,
	cfg map[string]any,
	dryRun bool,
	now time.Time,
	scheduledAction string,
	cdtCache map[string]cachedTraffic,
	ecsCache map[string]statusResult,
	billingState map[string]any,
	forceBillingRefresh bool,
) *CheckResult {
	name := config.GetStr(user, "name")
	if name == "" {
		name = config.GetStr(user, "instance_id")
	}
	if name == "" {
		name = "未命名"
	}
	billing := config.BillingConfig(user)
	schedule := GetScheduleConfig(user)
	var target any
	enabled, _ := schedule["enabled"].(bool)
	if enabled {
		target = ScheduleTarget(user, now)
	}
	res := &CheckResult{
		Name:              name,
		InstanceID:        config.GetStr(user, "instance_id"),
		LimitGB:           config.GetFloat(user, "traffic_limit_gb", 0),
		BillingEnabled:    toBool(billing["enabled"]),
		BillSymbol:        config.GetStr(billing, "currency_symbol"),
		Action:            "none",
		Level:             "ok",
		Errors:            []string{},
		Paused:            toBool(user["paused"]),
		ScheduleEnabled:   enabled,
		ScheduleStartTime: fmt.Sprintf("%v", schedule["start_time"]),
		ScheduleStopTime:  fmt.Sprintf("%v", schedule["stop_time"]),
		ScheduleTimezone:  fmt.Sprintf("%v", schedule["timezone"]),
		ScheduleTarget:    target,
		ScheduleEvent:     scheduledAction,
	}
	if res.Paused {
		res.Level = "paused"
		res.Message = "监控已暂停"
		Logger.Printf("[%s] 监控已暂停", name)
		return res
	}

	ak := config.GetStr(user, "ak")
	sk := config.GetStr(user, "sk")
	region := config.GetStr(user, "region")
	instanceID := config.GetStr(user, "instance_id")

	// CDT 帳號級流量查詢（同憑證於單輪內重複使用快取，含負快取）
	traffic, err := cdtTrafficForCycle(ak, sk, cdtCache)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("CDT 流量查询失败: %v", err))
		Logger.Errorf("[%s] CDT 流量查询失败: %v", name, err)
	} else {
		res.TrafficGB = &traffic
	}

	// ECS 狀態查詢：優先取本輪批量結果
	var status string
	if ecsCache != nil {
		entry, ok := ecsCache[statusCacheKey(ak, sk, region, instanceID)]
		if !ok {
			err = fmt.Errorf("ECS 批量查询缺少实例结果")
		} else if entry.err != nil {
			err = entry.err
		} else {
			status = entry.status
		}
	} else {
		status, err = queryStatusFn(ak, sk, region, instanceID)
	}
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("ECS 实例查询失败: %v", err))
		Logger.Errorf("[%s] ECS 实例查询失败: %v", name, err)
	} else {
		res.StatusBefore = status
		res.StatusAfter = status
	}
	coreErrCount := len(res.Errors)

	// 帳單查詢（帶快取；失敗不影響決策，僅記錄）
	if res.BillingEnabled {
		amount, currency, checkedAt, fromCache, billErr := queryBillCached(
			user, billing, billingState,
			config.GetInt(cfg, "billing_cache_seconds", 3600),
			forceBillingRefresh, now,
		)
		if amount != nil {
			res.BillAmount = amount
			res.BillCurrency = currency
			res.BillCheckedAt = checkedAt
			res.BillFromCache = fromCache
			Logger.Printf("[%s] 本月实例账单 %s%.2f (%s)%s",
				name, res.BillSymbol, *amount, currency, map[bool]string{true: "（缓存）", false: ""}[fromCache])
		}
		if billErr != nil {
			res.BillError = fmt.Sprintf("BSS 账单刷新失败，继续使用缓存: %v", billErr)
			if amount == nil {
				res.BillError = fmt.Sprintf("BSS 账单查询失败: %v", billErr)
			}
			res.Errors = append(res.Errors, res.BillError)
			// 與上游一致：仍有快取可用（amount != nil）記 warning，否則記 error。
			if amount != nil {
				Logger.Warningf("[%s] %s", name, res.BillError)
			} else {
				Logger.Errorf("[%s] %s", name, res.BillError)
			}
		}
	}

	actionsEnabled := toBool2(user["actions_enabled"], true)
	statusBefore := res.StatusBefore
	stopWait := config.GetInt(cfg, "stop_wait_seconds", 45)
	startWait := config.GetInt(cfg, "start_wait_seconds", 90)
	pollSeconds := config.GetInt(cfg, "start_poll_seconds", 5)

	// 排程關機時段：只依賴可讀的 ECS 狀態，不等 CDT/BSS
	if fmt.Sprintf("%v", target) == "stopped" && statusBefore != "" {
		switch statusBefore {
		case "Running":
			res.Action = "schedule_stop"
			switch {
			case dryRun:
				res.Level = "action"
				res.Message = "演练：当前处于计划关机时段，应停止实例"
			case !actionsEnabled:
				res.Level = "warning"
				res.Message = "当前处于计划关机时段，但自动操作未启用"
			default:
				if err := stopFn(ak, sk, region, instanceID); err != nil {
					res.Level = "error"
					res.Message = fmt.Sprintf("定时关机失败: %v", err)
					res.Errors = append(res.Errors, res.Message)
					Logger.Errorf("[%s] %s", name, res.Message)
				} else {
					res.ActionPerformed = true
					Logger.Printf("[%s] 已提交定时关机请求", name)
					latest, pollErr := waitForStatus(ak, sk, region, instanceID, "Stopped", stopWait, pollSeconds)
					applyPollResult(res, latest, pollErr, pollOutcome{
						confirmed: "定时关机已执行并确认实例停止",
						submitted: "已提交定时关机",
						wait:      stopWait,
					})
				}
			}
		case "Stopped":
			res.Message = "当前处于计划关机时段，实例保持关机"
		default:
			res.Level = "warning"
			res.Message = fmt.Sprintf("当前处于计划关机时段，实例状态为 %s，本轮不重复操作", statusBefore)
		}
		if len(res.Errors) > 0 {
			res.Level = "error"
		}
		Logger.Printf("[%s] 计划关机时段，状态 %s，结果: %s", name, statusBefore, res.Message)
		return res
	}

	// CDT 或 ECS 核心查詢失敗 → 本輪不執行開關機
	if coreErrCount > 0 {
		res.Level = "error"
		res.Message = "CDT 或 ECS 核心查询失败，本轮未执行开关机"
		return res
	}

	traffic = *res.TrafficGB // 重用先前 CDT 查詢宣告的 traffic 變數
	limit := res.LimitGB

	if traffic < limit {
		switch statusBefore {
		case "Running":
			if scheduledAction == "start" {
				res.Message = "已进入计划运行时段，实例正在运行"
			} else {
				res.Message = "流量安全，实例运行正常"
			}
		case "Stopped":
			if scheduledAction == "start" {
				res.Action = "schedule_start"
			} else {
				res.Action = "start"
			}
			switch {
			case dryRun:
				res.Level = "action"
				if enabled {
					res.Message = "演练：当前处于计划运行时段，应启动实例"
				} else {
					res.Message = "演练：应启动实例"
				}
			case !actionsEnabled:
				res.Level = "warning"
				if enabled {
					res.Message = "当前处于计划运行时段，但自动操作未启用"
				} else {
					res.Message = "流量安全但实例已停止，自动操作未启用"
				}
			default:
				if err := startFn(ak, sk, region, instanceID); err != nil {
					res.Level = "error"
					res.Message = fmt.Sprintf("启动实例失败: %v", err)
					res.Errors = append(res.Errors, res.Message)
					Logger.Errorf("[%s] %s", name, res.Message)
				} else {
					res.ActionPerformed = true
					startKind := "保活"
					if scheduledAction == "start" {
						startKind = "定时"
					}
					Logger.Printf("[%s] 已提交%s启动请求", name, startKind)
					latest, pollErr := waitForStatus(ak, sk, region, instanceID, "Running", startWait, pollSeconds)
					if scheduledAction == "start" {
						applyPollResult(res, latest, pollErr, pollOutcome{
							confirmed: "定时开机已执行并确认实例运行",
							submitted: "已提交定时启动请求",
							wait:      startWait,
						})
					} else {
						applyPollResult(res, latest, pollErr, pollOutcome{
							confirmed: "已启动并确认实例运行",
							submitted: "已提交保活启动请求",
							wait:      startWait,
						})
					}
				}
			}
		default:
			res.Level = "warning"
			res.Message = fmt.Sprintf("流量安全，实例处于过渡状态 %s，本轮不操作", statusBefore)
		}
	} else {
		switch statusBefore {
		case "Running":
			res.Action = "stop"
			switch {
			case dryRun:
				res.Level = "action"
				res.Message = "演练：流量达到阈值，应停止实例"
			case !actionsEnabled:
				res.Level = "warning"
				res.Message = "流量达到阈值，但自动操作未启用"
			default:
				if err := stopFn(ak, sk, region, instanceID); err != nil {
					res.Level = "error"
					res.Message = fmt.Sprintf("停止实例失败: %v", err)
					res.Errors = append(res.Errors, res.Message)
					Logger.Errorf("[%s] %s", name, res.Message)
				} else {
					res.ActionPerformed = true
					Logger.Warningf("[%s] 流量达到阈值，已提交停止请求", name)
					latest, pollErr := waitForStatus(ak, sk, region, instanceID, "Stopped", stopWait, pollSeconds)
					applyPollResult(res, latest, pollErr, pollOutcome{
						confirmed: "流量达到阈值，已停止并确认实例关机",
						submitted: "已提交停止请求",
						wait:      stopWait,
					})
				}
			}
		case "Stopped":
			res.Level = "warning"
			res.Message = "流量达到阈值，实例保持关机"
		default:
			res.Level = "warning"
			res.Message = fmt.Sprintf("流量达到阈值，实例状态为 %s，本轮不重复操作", statusBefore)
		}
	}

	if len(res.Errors) > 0 {
		res.Level = "error"
	}
	Logger.Printf("[%s] 流量 %.2f/%.2f GB，状态 %s，结果: %s", name, traffic, limit, statusBefore, res.Message)
	return res
}

// pollOutcome 描述開關機後輪詢結果的訊息模板，措辭與 Python 版逐字一致。
type pollOutcome struct {
	confirmed string // 輪詢到期望狀態時的訊息
	submitted string // 已提交前綴（用於複查失敗/等待訊息）
	wait      int    // 等待秒數（用於等待訊息）
}

// applyPollResult 依輪詢結果組裝訊息與狀態，對應 check_one 各輪詢分支。
func applyPollResult(res *CheckResult, latest string, pollErr error, outcome pollOutcome) {
	if pollErr != nil {
		res.Level = "warning"
		res.Message = fmt.Sprintf("%s，状态复查失败: %v", outcome.submitted, pollErr)
	} else if latest == outcome.confirmed || latest == expectedOf(outcome.confirmed) {
		res.Level = "action"
		res.Message = outcome.confirmed
	} else {
		res.Level = "warning"
		status := latest
		if status == "" {
			status = "Unknown"
		}
		res.Message = fmt.Sprintf("%s，等待 %d 秒后状态为 %s", outcome.submitted, outcome.wait, status)
	}
	if latest != "" {
		res.StatusAfter = latest
	}
}

// expectedOf 依確認訊息推導期望狀態（僅用於 applyPollResult 內部判斷）。
func expectedOf(confirmed string) string {
	if confirmed == "流量达到阈值，已停止并确认实例关机" || confirmed == "定时关机已执行并确认实例停止" {
		return "Stopped"
	}
	return "Running"
}

// waitForStatus 輪詢實例狀態直到期望狀態或逾時，對應 wait_for_status()。
// 回傳（最後觀察到的狀態, 輪詢錯誤）。
func waitForStatus(ak, sk, region, instanceID, expected string, timeoutSec, pollSec int) (string, error) {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	var last string
	for {
		status, err := queryStatusFn(ak, sk, region, instanceID)
		if err == nil && status != "" {
			last = status
			if status == expected {
				return last, nil
			}
		} else if err != nil {
			return last, err
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		time.Sleep(time.Duration(pollSec) * time.Second)
	}
}
