// 手動開關機操作，對應 web_panel.control_instance()：供 Telegram Bot（M2）
// 與網路面板（M3）共用。與週期檢測互斥（cycle 鎖 + config 鎖），耦合監控
// 暫停狀態（手動關機 → 自動暫停監控；手動開機 → 自動恢復監控），並寫入
// state 的 instances 與 history。
package guard

import (
	"fmt"
	"sync"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/state"
	"aliyun-guard-go/internal/xtime"
)

// configMu 序列化所有「讀取-修改-寫回 config.json」的交易，對應 config_lock()。
var configMu sync.Mutex

// ManualError 攜帶已格式化的完整中文訊息，呼叫端直接展示。
type ManualError struct{ Message string }

func (e *ManualError) Error() string { return e.Message }

// ControlResult 對應 control_instance() 的回傳 dict。
type ControlResult struct {
	Before              string
	After               string
	Message             string
	ThresholdOverridden bool
	MonitorPaused       bool
	MonitorResumed      bool
}

// ControlInstance 手動對 users[index] 執行 start/stop。
// source 為操作來源文案（如「Telegram Bot」/「网页控制台」）；notify 為 false
// 時不發送 Telegram 通知（Bot 場景自行編輯面板訊息）。
func ControlInstance(
	paths config.Paths,
	index int,
	action, source string,
	notifyFlag, allowThresholdOverride, pauseOnThresholdOverride bool,
) (*ControlResult, error) {
	if action != "start" && action != "stop" {
		return nil, &ManualError{"开关机动作无效"}
	}
	release, locked := acquireCycleLock(paths)
	if !locked {
		return nil, &ManualError{"检测任务正在运行，请稍后再试"}
	}
	defer release()

	configMu.Lock()
	defer configMu.Unlock()

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &ManualError{fmt.Sprintf("配置读取失败: %v", err)}
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return nil, &ManualError{"实例不存在"}
	}
	user := users[index]
	name := config.GetStr(user, "name")
	if name == "" {
		name = config.GetStr(user, "instance_id")
	}
	ak, sk := config.GetStr(user, "ak"), config.GetStr(user, "sk")
	region, instanceID := config.GetStr(user, "region"), config.GetStr(user, "instance_id")

	before := ""
	after := ""
	var traffic *float64
	performed := false
	var pollErr error
	thresholdOverridden := false
	monitorPaused := false
	monitorResumed := false
	verb := "关机"
	if action == "start" {
		verb = "开机"
	}

	fail := func(err error) error {
		manual, ok := err.(*ManualError)
		message := fmt.Sprintf("%s手动%s未执行: %v", source, verb, err)
		level := "error"
		if ok {
			message = manual.Message
		} else {
			message = fmt.Sprintf("实例%s失败: %v", verb, err)
			if monitorPaused {
				message += "；该实例监控已自动暂停，请处理后按需恢复"
			} else if monitorResumed {
				message += "；该实例监控已自动恢复"
			}
		}
		writeManualInstanceLog(paths, user, action, before, after, traffic, performed, message, level, source)
		return &ManualError{message}
	}

	before, err = aliyun.QueryInstanceStatus(ak, sk, region, instanceID)
	if err != nil {
		return nil, fail(err)
	}
	if action == "start" {
		t, err := aliyunListTraffic(ak, sk)
		if err != nil {
			return nil, fail(err)
		}
		traffic = &t
		limit := config.GetFloat(user, "traffic_limit_gb", 0)
		if t >= limit {
			if !allowThresholdOverride {
				return nil, fail(&ManualError{fmt.Sprintf(
					"当前 CDT 流量 %.2f GB 已达到 %.2f GB 阈值，拒绝开机", t, limit)})
			}
			thresholdOverridden = true
		}
		if thresholdOverridden && pauseOnThresholdOverride {
			if _, err := setInstanceMonitorState(paths, user, true); err != nil {
				return nil, fail(err)
			}
			monitorPaused = true
		}
		if before != "Running" {
			if err := aliyun.StartInstance(ak, sk, region, instanceID); err != nil {
				return nil, fail(err)
			}
			performed = true
			if !thresholdOverridden {
				monitorResumed, err = setInstanceMonitorState(paths, user, false)
				if err != nil {
					return nil, fail(err)
				}
			}
			after, pollErr = waitForStatus(ak, sk, region, instanceID, "Running",
				config.GetInt(cfg, "start_wait_seconds", 90), config.GetInt(cfg, "start_poll_seconds", 5))
		} else {
			after = before
			if !thresholdOverridden {
				monitorResumed, err = setInstanceMonitorState(paths, user, false)
				if err != nil {
					return nil, fail(err)
				}
			}
		}
	} else if before != "Stopped" {
		if err := aliyun.StopInstance(ak, sk, region, instanceID); err != nil {
			return nil, fail(err)
		}
		performed = true
		if monitorPaused, err = setInstanceMonitorState(paths, user, true); err != nil {
			return nil, fail(err)
		}
		after, pollErr = waitForStatus(ak, sk, region, instanceID, "Stopped",
			config.GetInt(cfg, "stop_wait_seconds", 45), config.GetInt(cfg, "start_poll_seconds", 5))
	} else {
		after = before
		if monitorPaused, err = setInstanceMonitorState(paths, user, true); err != nil {
			return nil, fail(err)
		}
	}

	afterDisplay := after
	if afterDisplay == "" {
		afterDisplay = "Unknown"
	}
	message := fmt.Sprintf("%s手动%s\n实例: %s (%s)\n状态: %s -> %s",
		source, verb, name, instanceID, before, afterDisplay)
	if pollErr != nil {
		message += fmt.Sprintf("\n状态复查: %v", pollErr)
	}
	if monitorPaused {
		if thresholdOverridden {
			message += "\n监控: 已自动暂停（流量阈值强制开机）"
		} else {
			message += "\n监控: 已自动暂停（手动关机后不会被自动开机）"
		}
	} else if monitorResumed {
		message += "\n监控: 已自动恢复"
	}

	// 通知（Bot 場景 notify=false，面板場景預設 true）
	if notifyFlag {
		if err := notifyTelegram(cfg, message); err != nil {
			Logger.Errorf("[Telegram] 手動操作通知发送失败: %v", err)
		}
	}

	level := "action"
	if !performed {
		level = "ok"
	}
	if pollErr != nil {
		level = "warning" // 狀態复查失敗計入 log_errors，等級上調
	}
	// traffic 缺省沿用前值（stop 路徑未查 CDT）
	if traffic == nil {
		if previous := loadInstancePrevious(paths.State, instanceID); previous != nil {
			if f, ok := previous["traffic_gb"].(float64); ok {
				traffic = &f
			}
		}
	}
	writeManualInstanceLog(paths, user, action, before, after, traffic, performed, message, level, source)
	updateInstanceStateAfterManual(paths, user, instanceID, name, traffic, before, after, message, level, action)

	return &ControlResult{
		Before:              before,
		After:               afterDisplay,
		Message:             message,
		ThresholdOverridden: thresholdOverridden,
		MonitorPaused:       monitorPaused,
		MonitorResumed:      monitorResumed,
	}, nil
}

// setInstanceMonitorState 將監控暫停狀態耦合寫回 config.json，對應
// _set_instance_monitor_state()；回傳狀態是否實際變化。
func setInstanceMonitorState(paths config.Paths, user map[string]any, paused bool) (bool, error) {
	instanceID := config.GetStr(user, "instance_id")
	region := config.GetStr(user, "region")
	latest, err := config.Load(paths.Config)
	if err != nil {
		return false, &ManualError{fmt.Sprintf("配置读取失败: %v", err)}
	}
	latestUser := map[string]any(nil)
	for _, item := range config.Users(latest) {
		if config.GetStr(item, "instance_id") == instanceID && config.GetStr(item, "region") == region {
			latestUser = item
			break
		}
	}
	if latestUser == nil {
		return false, &ManualError{"实例配置已经变化，请重新操作"}
	}
	changed := config.GetBool(latestUser, "paused", false) != paused
	latestUser["paused"] = paused
	if changed {
		if err := config.Save(paths.Config, latest); err != nil {
			return false, &ManualError{fmt.Sprintf("配置写入失败: %v", err)}
		}
	}
	user["paused"] = paused
	return changed, nil
}

// writeManualInstanceLog 記錄手動操作：主日誌一行 + per-instance 獨立日誌
// （instance_log_enabled 時），對應 _write_manual_instance_log。
func writeManualInstanceLog(
	paths config.Paths, user map[string]any, action, before, after string,
	traffic *float64, performed bool, message, level, source string,
) {
	verb := "关机"
	if action == "start" {
		verb = "开机"
	}
	Logger.Printf("[%s] %s手动%s: %s", config.GetStr(user, "instance_id"), source, verb, message)
	var errs []string
	if level == "warning" || level == "error" {
		errs = []string{message}
	}
	WriteInstanceLog(paths, user,
		ManualLogEntry(user, action, before, after, traffic, performed, message, level, errs),
		false, source+"手动"+verb)
}

// updateInstanceStateAfterManual 將手動操作結果寫入 state 的 instances 與
// history（action 為 manual_start / manual_stop），對應 control_instance() 尾段。
func updateInstanceStateAfterManual(
	paths config.Paths, user map[string]any, instanceID, name string,
	traffic *float64, before, after, message, level, action string,
) {
	st, err := state.Load(paths.State)
	if err != nil {
		return
	}
	checkedAt := time.Now().Format(xtime.ISOFormat)
	instances, _ := st["instances"].(map[string]any)
	if instances == nil {
		instances = map[string]any{}
		st["instances"] = instances
	}
	previous, _ := instances[instanceID].(map[string]any)
	merged := map[string]any{}
	for k, v := range previous {
		merged[k] = v
	}
	merged["name"] = name
	merged["traffic_gb"] = traffic
	merged["status_after"] = afterOr(before, after)
	merged["level"] = level
	merged["message"] = replaceNewlines(message)
	merged["checked_at"] = checkedAt
	instances[instanceID] = merged

	history, _ := st["history"].([]any)
	history = append(history, map[string]any{
		"at": checkedAt,
		"instances": map[string]any{
			instanceID: map[string]any{
				"traffic_gb":       traffic,
				"status":           afterOr(before, after),
				"status_before":    before,
				"status_after":     afterOr(before, after),
				"action":           "manual_" + action,
				"action_performed": level == "action",
				"message":          replaceNewlines(message),
				"level":            level,
			},
		},
	})
	if len(history) > 576 {
		history = history[len(history)-576:]
	}
	st["history"] = history
	_ = state.Save(paths.State, st)
}

func afterOr(before, after string) string {
	if after != "" {
		return after
	}
	if before != "" {
		return before
	}
	return "Unknown"
}

// loadInstancePrevious 讀取 state 中指定實例的前次狀態（無則回傳 nil）。
func loadInstancePrevious(statePath, instanceID string) map[string]any {
	st, err := state.Load(statePath)
	if err != nil {
		return nil
	}
	instances, _ := st["instances"].(map[string]any)
	prev, _ := instances[instanceID].(map[string]any)
	return prev
}

// notifyTelegram 發送手動操作通知（對應 send_telegram_message）。
func notifyTelegram(cfg map[string]any, text string) error {
	return notify.SendMessage(cfg, text)
}

func replaceNewlines(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, "；"...)
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}
