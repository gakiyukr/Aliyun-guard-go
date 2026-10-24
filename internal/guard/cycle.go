// 本檔對應 Python 版 run_cycle / update_state / prefetch_instance_statuses /
// is_due / scheduler_wait_seconds / cdt_monthly_reset_due 等調度層函式。
package guard

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/state"
	"aliyun-guard-go/internal/xtime"
)

// cachedTraffic 單輪檢測內的 CDT 帳號級流量快取（含負快取：失敗也快取），
// 對應 query_cdt_traffic_gb_for_cycle 的 cycle_cache。
type cachedTraffic struct {
	gb  float64
	err error
}

// statusResult 批量 ECS 狀態查詢的單實例結果（含錯誤，對應 cycle cache 的 (None, err)）。
type statusResult struct {
	status string
	err    error
}

// maxHistory 對應 Python 版 history[-576:] 的環形上限。
const maxHistory = 576

// 批量查詢鉤子，測試中可替換。
var batchStatusFn = aliyun.DescribeInstances

func cdtTrafficForCycle(ak, sk string, cache map[string]cachedTraffic) (float64, error) {
	sum := sha256.Sum256([]byte(ak + "\x00" + sk))
	key := string(sum[:])
	if cache != nil {
		if c, ok := cache[key]; ok {
			return c.gb, c.err
		}
	}
	gb, err := aliyunListTraffic(ak, sk)
	if cache != nil {
		cache[key] = cachedTraffic{gb: gb, err: err}
	}
	return gb, err
}

// aliyunListTraffic 包裝 CDT 流量查詢，var 形式便於測試時替換。
var aliyunListTraffic = func(ak, sk string) (float64, error) {
	return aliyun.ListCdtInternetTraffic(ak, sk)
}

// statusCacheKey 批量狀態快取的 key：憑證 + region + 實例 ID，對應 ecs_status_cache_key()。
func statusCacheKey(ak, sk, region, instanceID string) string {
	return ak + "\x00" + sk + "\x00" + region + "\x00" + instanceID
}

// prefetchStatuses 對應 query_instance_statuses / prefetch_instance_statuses：
// 依（憑證, region）分組，每組一次 DescribeInstances 批量查詢。
// 回傳 map[key]statusResult；無使用者時回傳 nil（CheckOne 退回單查路徑）。
func prefetchStatuses(users []map[string]any) map[string]statusResult {
	if len(users) == 0 {
		return nil
	}
	type group struct {
		ak, sk, region string
		ids            []string
	}
	groups := map[string]*group{}
	order := []string{}
	for _, u := range users {
		ak, sk := config.GetStr(u, "ak"), config.GetStr(u, "sk")
		region, id := config.GetStr(u, "region"), config.GetStr(u, "instance_id")
		if id == "" {
			continue
		}
		key := ak + "\x00" + sk + "\x00" + region
		g, ok := groups[key]
		if !ok {
			g = &group{ak: ak, sk: sk, region: region}
			groups[key] = g
			order = append(order, key)
		}
		g.ids = append(g.ids, id)
	}
	out := map[string]statusResult{}
	for _, key := range order {
		g := groups[key]
		statuses, err := batchStatusFn(g.ak, g.sk, g.region, g.ids)
		for _, id := range g.ids {
			k := statusCacheKey(g.ak, g.sk, g.region, id)
			switch {
			case err != nil:
				out[k] = statusResult{"", err}
			case statuses == nil:
				out[k] = statusResult{"", fmt.Errorf("ECS 批量查询缺少实例结果")}
			default:
				if s, ok := statuses[id]; ok && s != "" {
					out[k] = statusResult{s, nil}
				} else {
					out[k] = statusResult{"", fmt.Errorf("区域 %s 中未找到实例 %s", g.region, id)}
				}
			}
		}
	}
	return out
}

// IsDue 判斷是否該執行完整檢測輪，對應 is_due()：優先 epoch，fallback ISO 時間戳。
func IsDue(cfg, st map[string]any, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	lastAny, exists := st["last_cycle_epoch"]
	if !exists || lastAny == nil || lastAny == float64(0) {
		lastAny = st["last_cycle_finished_at"]
	}
	if lastAny == nil {
		return true
	}
	last, ok := xtime.ParseStamp(lastAny)
	if !ok {
		return true
	}
	return now.Sub(last).Seconds() >= float64(config.GetInt(cfg, "interval_seconds", 300))
}

// MonthlyResetDue 判斷 CDT 月結算補檢是否到期，對應 cdt_monthly_reset_due()：
// 三級 fallback——cdt_month_checked 字串 → ISO 時間戳 → epoch。
func MonthlyResetDue(st map[string]any, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	currentMonth := xtime.MonthKey(now)
	if checked, _ := st["cdt_month_checked"].(string); checked != "" {
		return checked != currentMonth
	}
	previous := st["last_cycle_started_at"]
	if previous == nil {
		previous = st["last_cycle_finished_at"]
	}
	if previous != nil {
		if t, ok := xtime.ParseStamp(previous); ok {
			return xtime.MonthKey(t) != currentMonth
		}
	}
	if epoch, ok := st["last_cycle_epoch"].(float64); ok {
		return xtime.MonthKey(time.Unix(int64(epoch), 0)) != currentMonth
	}
	return false
}

// SchedulerWait 計算守護迴圈睡眠秒數，對應 scheduler_wait_seconds()：
// 取「距下次完整檢測」與「距下一分鐘邊界」較小者，確保每分鐘醒來檢查排程。
func SchedulerWait(cfg, st map[string]any) time.Duration {
	now := float64(time.Now().UnixNano()) / 1e9
	regular := 60.0
	if last, ok := st["last_cycle_epoch"].(float64); ok && last > 0 {
		interval := float64(config.GetInt(cfg, "interval_seconds", 300))
		regular = interval - (now - last)
		if regular < 1 {
			regular = 1
		}
	}
	minuteWait := 60.05 - math.Mod(now, 60)
	wait := math.Min(regular, minuteWait)
	if wait < 1 {
		wait = 1
	}
	return time.Duration(wait * float64(time.Second))
}

// RunCycle 執行一輪完整檢測：批量預查 ECS 狀態、逐實例 CheckOne、
// 更新 state（含 history 環）、組裝彙總並通知。
func RunCycle(paths config.Paths, cfg, st map[string]any, dryRun, noNotify bool) []*CheckResult {
	now := time.Now()
	startedAt := now
	start := time.Now()

	// run_id / run_result 對稱日誌（借鑑上游 spot_ddns 主流程模式）：
	// 每輪以固定格式開始/結束各一行，result 為機器可讀枚舉，便於事後 grep 統計。
	runID := startedAt.Format("20060102-150405")
	if dryRun {
		runID += "-dry"
	}
	Logger.Printf("===== 检测轮开始 run_id=%s at=%s =====", runID, startedAt.Format(xtime.ISOFormat))

	users := config.Users(cfg)
	instances, _ := st["instances"].(map[string]any)
	if instances == nil {
		instances = map[string]any{}
	}
	prevInstances := instances // should_notify 需要比對更新前的狀態

	// 補檢旗標：跨月第一輪強制刷新帳單快取（cdt_month_checked 由 updateState 統一記錄）
	monthlyDue := MonthlyResetDue(st, now)

	ecsCache := prefetchStatuses(users)
	cdtCache := map[string]cachedTraffic{}
	results := make([]*CheckResult, 0, len(users))
	for _, user := range users {
		instanceID := config.GetStr(user, "instance_id")
		prev, _ := prevInstances[instanceID].(map[string]any)
		action := ScheduleTransition(user, prev, now)
		result := CheckOne(user, cfg, dryRun, now, action, cdtCache, ecsCache, st, monthlyDue && !dryRun)
		results = append(results, result)
		// per-instance 獨立日誌（instance_log_enabled 時逐筆寫入）
		WriteInstanceLog(paths, user, CheckResultEntry(result), dryRun, "周期检测")
	}

	summary := BuildSummary(results, startedAt, dryRun)

	// 通知（notification_mode：always / events / errors；errors 模式下有錯誤仍通知）
	notifyErr := error(nil)
	if !noNotify && ShouldNotify(cfg, results, prevInstances) {
		if err := notifySummary(cfg, results, startedAt, dryRun); err != nil {
			notifyErr = err
			Logger.Errorf("[Telegram] 通知发送失败: %v", err)
		}
	}

	updateState(st, results, startedAt, time.Since(start), summary, notifyErr, dryRun)
	_ = state.Save(paths.State, st)

	// 結束日誌：result 枚舉（errors=N 為實例錯誤數，notify_failed 為通知失敗）
	errorCount := 0
	actions := 0
	for _, r := range results {
		if r.Level == "error" {
			errorCount++
		}
		if r.ActionPerformed {
			actions++
		}
	}
	resultStr := "ok"
	switch {
	case errorCount > 0:
		resultStr = fmt.Sprintf("errors=%d", errorCount)
	case dryRun:
		resultStr = "dry_run"
	}
	if actions > 0 {
		resultStr += fmt.Sprintf(" actions=%d", actions)
	}
	if notifyErr != nil {
		resultStr += " notify_failed"
	}
	Logger.Printf("===== 检测轮结束 run_id=%s result=%s instances=%d cost=%.2fs =====",
		runID, resultStr, len(results), time.Since(start).Seconds())
	return results
}

// updateState 對應 Python 版 update_state()：
//   - last_cycle_epoch 與 cdt_month_checked 僅在非 dry-run 輪寫入
//   - schedule_signature / schedule_target 僅在非 dry-run 且未暫停時更新，否則沿用前值
//   - 非 dry-run 輪追加 history 環（上限 576 筆）
func updateState(
	st map[string]any,
	results []*CheckResult,
	startedAt time.Time,
	duration time.Duration,
	summary string,
	notifyErr error,
	dryRun bool,
) {
	finishedAt := time.Now()
	st["last_cycle_started_at"] = startedAt.Format(xtime.ISOFormat)
	st["last_cycle_finished_at"] = finishedAt.Format(xtime.ISOFormat)
	st["last_cycle_duration_seconds"] = math.Round(duration.Seconds()*1000) / 1000
	errorCount := 0
	for _, r := range results {
		if r.Level == "error" {
			errorCount++
		}
	}
	st["last_cycle_error_count"] = errorCount
	st["last_cycle_ok"] = errorCount == 0
	st["last_summary"] = summary
	st["cycle_count"] = toInt(st["cycle_count"]) + 1
	if notifyErr != nil {
		st["telegram_error"] = notifyErr.Error()
	} else {
		st["telegram_error"] = nil
	}
	if !dryRun {
		st["last_cycle_epoch"] = startedAt.Unix()
		st["cdt_month_checked"] = xtime.MonthKey(startedAt)
	}

	instances, _ := st["instances"].(map[string]any)
	if instances == nil {
		instances = map[string]any{}
		st["instances"] = instances
	}
	for _, item := range results {
		previous, _ := instances[item.InstanceID].(map[string]any)
		instanceState := map[string]any{
			"name":            item.Name,
			"traffic_gb":      item.TrafficGB,
			"limit_gb":        item.LimitGB,
			"status_after":    item.StatusAfter,
			"bill_amount":     item.BillAmount,
			"bill_currency":   item.BillCurrency,
			"bill_error":      nilIfEmpty(item.BillError),
			"bill_checked_at": item.BillCheckedAt,
			"bill_from_cache": item.BillFromCache,
			"level":           item.Level,
			"message":         item.Message,
			"checked_at":      startedAt.Format(xtime.ISOFormat),
		}
		if !dryRun && !item.Paused {
			if item.ScheduleEnabled {
				instanceState["schedule_signature"] = ComposeScheduleSignature(item.ScheduleStartTime, item.ScheduleStopTime, item.ScheduleTimezone)
			} else {
				instanceState["schedule_signature"] = "disabled"
			}
			instanceState["schedule_target"] = item.ScheduleTarget
		} else {
			// dry-run 或暫停：沿用前值，避免污染排程遷移偵測的指紋
			for _, field := range []string{"schedule_signature", "schedule_target"} {
				if v, ok := previous[field]; ok {
					instanceState[field] = v
				}
			}
		}
		instances[item.InstanceID] = instanceState
	}

	if !dryRun {
		history, _ := st["history"].([]any)
		entry := map[string]any{
			"at": startedAt.Format(xtime.ISOFormat),
			"instances": func() map[string]any {
				m := map[string]any{}
				for _, item := range results {
					m[item.InstanceID] = map[string]any{
						"traffic_gb":       item.TrafficGB,
						"status":           item.StatusAfter,
						"status_before":    item.StatusBefore,
						"status_after":     item.StatusAfter,
						"bill_amount":      item.BillAmount,
						"action":           item.Action,
						"action_performed": item.ActionPerformed,
						"message":          item.Message,
						"level":            item.Level,
					}
				}
				return m
			}(),
		}
		history = append(history, entry)
		if len(history) > maxHistory {
			history = history[len(history)-maxHistory:]
		}
		st["history"] = history
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func toInt(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// acquireCycleLock / releaseCycleLock 以鎖檔防止多輪檢測並行，對應 cycle_lock()。
// 鎖檔殘留超過 1 小時視為陳舊直接移除。
func acquireCycleLock(paths config.Paths) (func(), bool) {
	lockPath := filepath.Join(filepath.Dir(paths.Config), "cycle.lock")
	if info, err := os.Stat(lockPath); err == nil {
		if time.Since(info.ModTime()) < time.Hour {
			return nil, false
		}
		_ = os.Remove(lockPath)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, false
	}
	_, _ = f.WriteString(fmt.Sprintf("pid=%d\n", os.Getpid()))
	_ = f.Close()
	return func() { _ = os.Remove(lockPath) }, true
}
