// 面板變更操作（web_actions.py 子集）：實例增刪改查/驗證、全域設定、
// 面板設定、Telegram 身份、排程與暫停。s3 備份/節點代理/更新回滾/實例
// 探索已按需求裁剪。全部寫入操作持有 config 鎖，對應 _config_transaction。
package web

import (
	"fmt"
	"strings"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/guard"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/xtime"
)

// PanelError 攜帶 HTTP 狀態碼的面板錯誤（對應 WebPanelError/ManagementError）。
type PanelError struct {
	Message string
	Status  int
}

func (e *PanelError) Error() string { return e.Message }

// billingPresets 對應 BILLING_PRESETS。
var billingPresets = map[string]map[string]any{
	"china": {
		"enabled": true, "site": "china", "endpoint": "business.aliyuncs.com",
		"region": "cn-hangzhou", "currency_code": "CNY", "currency_symbol": "¥",
	},
	"international": {
		"enabled": true, "site": "international", "endpoint": "business.ap-southeast-1.aliyuncs.com",
		"region": "ap-southeast-1", "currency_code": "USD", "currency_symbol": "$",
	},
}

// --- 輸入輔助（對應 _required_text/_optional_secret/_boolean/_integer/_number）---

func requiredText(data map[string]any, name, current, label string) (string, error) {
	value := strings.TrimSpace(toString(orDefaultAny(data[name], current)))
	if value == "" {
		return "", &PanelError{label + "不能为空", 400}
	}
	return value, nil
}

func optionalSecret(data map[string]any, name, current, label string) (string, error) {
	value := strings.TrimSpace(toString(orDefaultAny(data[name], "")))
	if value != "" {
		return value, nil
	}
	if strings.TrimSpace(current) == "" {
		return "", &PanelError{label + "不能为空", 400}
	}
	return strings.TrimSpace(current), nil
}

// toString 將 any 安全轉字串（nil → ""）。
func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%f", s), "0"), "0")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func asBool(data map[string]any, name string, current bool) (bool, error) {
	value, ok := data[name]
	if !ok {
		value = current
	}
	if v, isBool := value.(bool); isBool {
		return v, nil
	}
	return false, &PanelError{name + "必须是布尔值", 400}
}

func asInt(data map[string]any, name string, current, minV, maxV int) (int, error) {
	value, ok := data[name]
	if !ok {
		value = current
	}
	var v int
	switch n := value.(type) {
	case float64:
		v = int(n)
	case int:
		v = n
	case string:
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &v); err != nil {
			return 0, &PanelError{name + "必须是整数", 400}
		}
	default:
		return 0, &PanelError{name + "必须是整数", 400}
	}
	if v < minV || v > maxV {
		return 0, &PanelError{fmt.Sprintf("%s必须在 %d 到 %d 之间", name, minV, maxV), 400}
	}
	return v, nil
}

func asNumber(data map[string]any, name string, current, minV float64) (float64, error) {
	value, ok := data[name]
	if !ok {
		value = current
	}
	var v float64
	switch n := value.(type) {
	case float64:
		v = n
	case int:
		v = float64(n)
	case string:
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &v); err != nil {
			return 0, &PanelError{name + "必须是数字", 400}
		}
	default:
		return 0, &PanelError{name + "必须是数字", 400}
	}
	if v < minV {
		return 0, &PanelError{fmt.Sprintf("%s不能小于 %g", name, minV), 400}
	}
	return v, nil
}

// saveConfig 校驗後原子寫回（對應 _save_config）。
func saveConfig(paths config.Paths, cfg map[string]any) error {
	if err := config.Save(paths.Config, cfg); err != nil {
		return &PanelError{fmt.Sprintf("配置写入失败: %v", err), 500}
	}
	return nil
}

// --- 計費/排程正規化（對應 _normalize_billing/_normalize_schedule）---

func normalizeBilling(raw map[string]any, existing map[string]any) (map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	current := config.BillingConfig(existing)
	enabled, err := asBool(raw, "enabled", config.GetBool(current, "enabled", true))
	if err != nil {
		return nil, err
	}
	if !enabled {
		result := map[string]any{}
		for k, v := range current {
			result[k] = v
		}
		result["enabled"] = false
		return result, nil
	}
	site := orEmptyDefault(strings.TrimSpace(toString(orDefaultAny(raw["site"], current["site"]))), "china")
	if preset, ok := billingPresets[site]; ok {
		result := map[string]any{}
		for k, v := range preset {
			result[k] = v
		}
		return result, nil
	}
	if site != "custom" {
		return nil, &PanelError{"账单站点无效", 400}
	}
	result := map[string]any{"enabled": true, "site": "custom"}
	for field, label := range map[string]string{
		"endpoint": "BSS Endpoint", "region": "BSS 签名 Region",
		"currency_code": "币种代码", "currency_symbol": "币种符号",
	} {
		v, err := requiredText(raw, field, config.GetStr(current, field), label)
		if err != nil {
			return nil, err
		}
		result[field] = v
	}
	result["currency_code"] = strings.ToUpper(result["currency_code"].(string))
	return result, nil
}

func normalizeSchedule(raw map[string]any, existing map[string]any) (map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	current := guard.GetScheduleConfig(existing)
	enabled, err := asBool(raw, "enabled", current["enabled"] == true)
	if err != nil {
		return nil, err
	}
	startTime, errStart := guard.NormalizeScheduleTime(toString(orDefaultAny(raw["start_time"], current["start_time"])), "开机时间")
	if errStart != nil {
		return nil, errStart
	}
	stopTime, errStop := guard.NormalizeScheduleTime(toString(orDefaultAny(raw["stop_time"], current["stop_time"])), "关机时间")
	if errStop != nil {
		return nil, errStop
	}
	// 空值（未填寫）沿用現值，而非視為系統時區
	tz := strings.TrimSpace(toString(orDefaultAny(raw["timezone"], current["timezone"])))
	if tz == "" {
		tz = guard.NormalizeScheduleTimezone(current["timezone"])
	}
	if _, err := xtime.LoadScheduleLocation(tz); err != nil {
		return nil, &PanelError{err.Error(), 400}
	}
	if enabled && startTime == stopTime {
		return nil, &PanelError{"开机时间和关机时间不能相同", 400}
	}
	return map[string]any{"enabled": enabled, "start_time": startTime, "stop_time": stopTime, "timezone": tz}, nil
}

// --- 實例管理（對應 save_instance/validate_instance/delete_instance/update_instance_logging）---

func buildInstanceCandidate(data map[string]any, existing map[string]any) (map[string]any, error) {
	if existing == nil {
		existing = map[string]any{}
	}
	candidate := map[string]any{}
	for k, v := range existing {
		candidate[k] = v
	}
	var err error
	if candidate["name"], err = requiredText(data, "name", config.GetStr(existing, "name"), "备注名称"); err != nil {
		return nil, err
	}
	if candidate["ak"], err = optionalSecret(data, "ak", config.GetStr(existing, "ak"), "AccessKey ID"); err != nil {
		return nil, err
	}
	if candidate["sk"], err = optionalSecret(data, "sk", config.GetStr(existing, "sk"), "AccessKey Secret"); err != nil {
		return nil, err
	}
	if candidate["region"], err = requiredText(data, "region", config.GetStr(existing, "region"), "Region ID"); err != nil {
		return nil, err
	}
	if candidate["instance_id"], err = requiredText(data, "instance_id", config.GetStr(existing, "instance_id"), "实例 ID"); err != nil {
		return nil, err
	}
	if candidate["traffic_limit_gb"], err = asNumber(data, "traffic_limit_gb", config.GetFloat(existing, "traffic_limit_gb", 180), 0.01); err != nil {
		return nil, err
	}
	if candidate["actions_enabled"], err = asBool(data, "actions_enabled", config.GetBool(existing, "actions_enabled", true)); err != nil {
		return nil, err
	}
	if candidate["instance_log_enabled"], err = asBool(data, "instance_log_enabled", config.GetBool(existing, "instance_log_enabled", false)); err != nil {
		return nil, err
	}
	candidate["paused"] = config.GetBool(existing, "paused", false)
	if candidate["billing"], err = normalizeBilling(orMap(data["billing"]), existing); err != nil {
		return nil, err
	}
	if candidate["schedule"], err = normalizeSchedule(orMap(data["schedule"]), existing); err != nil {
		return nil, err
	}
	for _, legacy := range []string{"bill_endpoint", "currency", "billing_enabled"} {
		delete(candidate, legacy)
	}
	return candidate, nil
}

func orMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// validateUserConnection 對應 validate_user_connection()：CDT/ECS/BSS 三路查詢。
func validateUserConnection(user map[string]any) map[string]any {
	result := map[string]any{
		"ok": false, "traffic_gb": nil, "status": nil,
		"bill_amount": nil, "bill_currency": nil,
		"billing_enabled": config.GetBool(config.BillingConfig(user), "enabled", true),
		"errors":          []any{},
	}
	ak, sk := config.GetStr(user, "ak"), config.GetStr(user, "sk")
	if traffic, err := aliyun.ListCdtInternetTraffic(ak, sk); err == nil {
		result["traffic_gb"] = traffic
	} else {
		result["errors"] = append(result["errors"].([]any), fmt.Sprintf("CDT 流量查询失败: %v", err))
	}
	if status, err := aliyun.QueryInstanceStatus(ak, sk, config.GetStr(user, "region"), config.GetStr(user, "instance_id")); err == nil {
		result["status"] = status
	} else {
		result["errors"] = append(result["errors"].([]any), fmt.Sprintf("ECS 实例查询失败: %v", err))
	}
	if result["billing_enabled"] == true {
		cycle := xtime.MonthKey(time.Now())
		if amount, currency, err := aliyun.DescribeInstanceBill(ak, sk, config.GetStr(config.BillingConfig(user), "endpoint"), config.GetStr(user, "instance_id"), cycle); err == nil {
			result["bill_amount"] = amount
			result["bill_currency"] = currency
		} else {
			result["errors"] = append(result["errors"].([]any), fmt.Sprintf("BSS 账单查询失败: %v", err))
		}
	}
	result["ok"] = len(result["errors"].([]any)) == 0
	return result
}

// saveInstance 新增或更新實例（含連線驗證；force_save 可跳過驗證失敗）。
func saveInstance(paths config.Paths, data map[string]any, index *int) (map[string]any, error) {
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	users := config.Users(cfg)
	var existing map[string]any
	if index != nil {
		if *index < 0 || *index >= len(users) {
			return nil, &PanelError{"实例不存在", 404}
		}
		existing = users[*index]
	}
	candidate, err := buildInstanceCandidate(data, existing)
	if err != nil {
		return nil, err
	}
	forceSave, err := asBool(data, "force_save", false)
	if err != nil {
		return nil, err
	}
	validation := validateUserConnection(candidate)
	candidateIndex := 0
	if index == nil {
		users = append(users, candidate)
		cfg["users"] = toAnySlice(users)
		candidateIndex = len(users) - 1
	} else {
		users[*index] = candidate
		candidateIndex = *index
	}
	if validation["ok"] != true && !forceSave {
		return nil, &PanelError{"实例校验未通过，配置尚未保存", 422}
	}
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return map[string]any{
		"saved": true, "index": candidateIndex,
		"validation": validation,
		"instance":   instancePayload(candidate, candidateIndex),
	}, nil
}

// validateInstance 驗證既有實例連線。
func validateInstance(paths config.Paths, index int) (map[string]any, error) {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return nil, &PanelError{"实例不存在", 404}
	}
	return validateUserConnection(users[index]), nil
}

// deleteInstance 刪除實例（需 instance_id 二次確認）。
func deleteInstance(paths config.Paths, index int, instanceID string) (map[string]any, error) {
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return nil, &PanelError{"实例不存在", 404}
	}
	currentID := config.GetStr(users[index], "instance_id")
	if instanceID != currentID {
		return nil, &PanelError{"删除确认信息不匹配", 409}
	}
	deleted := users[index]
	users = append(users[:index], users[index+1:]...)
	cfg["users"] = toAnySlice(users)
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return map[string]any{"name": config.GetStr(deleted, "name"), "instance_id": currentID}, nil
}

// updateInstanceLogging 切換實例日誌開關（JSONL 落地屬 M4）。
func updateInstanceLogging(paths config.Paths, index int, enabled any) (map[string]any, error) {
	flag, ok := enabled.(bool)
	if !ok {
		return nil, &PanelError{"enabled 必须是布尔值", 400}
	}
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return nil, &PanelError{"实例不存在", 404}
	}
	users[index]["instance_log_enabled"] = flag
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return map[string]any{"enabled": flag, "instance": instancePayload(users[index], index)}, nil
}

// updateSchedule 對應 update_schedule()。
func updateSchedule(paths config.Paths, index int, data map[string]any) (map[string]any, error) {
	enabled, err := asBool(data, "enabled", false)
	if err != nil {
		return nil, err
	}
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return nil, &PanelError{"实例不存在", 404}
	}
	schedule, err := normalizeSchedule(data, users[index])
	if err != nil {
		return nil, err
	}
	schedule["enabled"] = enabled
	if enabled {
		if schedule["start_time"] == schedule["stop_time"] {
			return nil, &PanelError{"开机时间和关机时间不能相同", 400}
		}
	}
	users[index]["schedule"] = schedule
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return schedule, nil
}

// updatePause 對應 update_pause()。
func updatePause(paths config.Paths, index int, paused any) (bool, error) {
	flag, ok := paused.(bool)
	if !ok {
		return false, &PanelError{"paused 必须是布尔值", 400}
	}
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return false, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return false, &PanelError{"实例不存在", 404}
	}
	users[index]["paused"] = flag
	if err := saveConfig(paths, cfg); err != nil {
		return false, err
	}
	return flag, nil
}

// updateGlobalSettings 對應 update_global_settings()。
func updateGlobalSettings(paths config.Paths, data map[string]any) (map[string]any, error) {
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	mode := orEmptyDefault(toString(orDefaultAny(data["notification_mode"], config.GetStr(cfg, "notification_mode"))), "always")
	if mode != "always" && mode != "events" && mode != "errors" {
		return nil, &PanelError{"通知模式无效", 400}
	}
	// watchdog 模組已放棄：欄位原樣保留不讀取
	if _, err := asInt(data, "interval_seconds", config.GetInt(cfg, "interval_seconds", 300), 60, 86400); err != nil {
		return nil, err
	}
	values := map[string]struct {
		current int
		min, ma int
	}{
		"interval_seconds":      {config.GetInt(cfg, "interval_seconds", 300), 60, 86400},
		"billing_cache_seconds": {config.GetInt(cfg, "billing_cache_seconds", 3600), 300, 86400},
		"start_wait_seconds":    {config.GetInt(cfg, "start_wait_seconds", 90), 0, 600},
		"stop_wait_seconds":     {config.GetInt(cfg, "stop_wait_seconds", 45), 0, 600},
		"start_poll_seconds":    {config.GetInt(cfg, "start_poll_seconds", 5), 1, 60},
	}
	for name, spec := range values {
		v, err := asInt(data, name, spec.current, spec.min, spec.ma)
		if err != nil {
			return nil, err
		}
		cfg[name] = v
	}
	cfg["notification_mode"] = mode
	if cfg["force_ipv4"], err = asBool(data, "force_ipv4", config.GetBool(cfg, "force_ipv4", true)); err != nil {
		return nil, err
	}
	if cfg["notify_on_daemon_start"], err = asBool(data, "notify_on_daemon_start", config.GetBool(cfg, "notify_on_daemon_start", false)); err != nil {
		return nil, err
	}
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	payload, err := managementPayload(paths, "unknown")
	if err != nil {
		return nil, err
	}
	return payload["settings"].(map[string]any), nil
}

// updateWebSettings 對應 update_web_settings()：啟停/監聽/帳號密碼。
func updateWebSettings(paths config.Paths, data map[string]any) (map[string]any, error) {
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	current := getWebConfig(cfg)
	enabled, err := asBool(data, "enabled", current["enabled"].(bool))
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(toString(orDefaultAny(data["host"], current["host"])))
	if host == "" {
		host = current["host"].(string)
	}
	if host != "127.0.0.1" && host != "0.0.0.0" {
		return nil, &PanelError{"网页监听地址无效", 400}
	}
	port, err := asInt(data, "port", current["port"].(int), 1024, 65535)
	if err != nil {
		return nil, err
	}
	if host == "0.0.0.0" && current["host"] != "0.0.0.0" && data["confirm_public"] != true {
		return nil, &PanelError{"监听所有网卡需要确认 HTTP 明文传输风险", 409}
	}
	username, err := requiredText(data, "username", current["username"].(string), "网页登录用户名")
	if err != nil {
		return nil, err
	}
	candidate := map[string]any{
		"enabled": enabled, "host": host, "port": port, "username": username,
		"password_hash": current["password_hash"], "cookie_secure": false,
	}
	password, _ := data["password"].(string)
	confirmation, _ := data["password_confirm"].(string)
	if password != "" || confirmation != "" {
		if password != confirmation {
			return nil, &PanelError{"两次输入的网页登录密码不一致", 400}
		}
		hash, err := hashPassword(password)
		if err != nil {
			return nil, &PanelError{err.Error(), 400}
		}
		candidate["password_hash"] = hash
	}
	if enabled && strings.TrimSpace(candidate["password_hash"].(string)) == "" {
		return nil, &PanelError{"启用网页面板前必须设置登录密码", 400}
	}
	cfg["web_panel"] = candidate
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	persisted := getWebConfig(mustLoad(paths))
	if persisted["password_hash"] != candidate["password_hash"] {
		return nil, &PanelError{"网页密码未能持久保存，请检查配置文件所在磁盘", 500}
	}
	restartRequired := candidate["enabled"] != current["enabled"] ||
		candidate["host"] != current["host"] || candidate["port"] != current["port"] ||
		candidate["username"] != current["username"] || candidate["password_hash"] != current["password_hash"]
	return map[string]any{
		"enabled": enabled, "host": host, "port": port, "username": username,
		"password_configured": strings.TrimSpace(candidate["password_hash"].(string)) != "",
		"restart_required":    restartRequired,
	}, nil
}

// updateTelegramIdentity 對應 update_telegram_identity()（節點/訂閱欄位不動）。
func updateTelegramIdentity(paths config.Paths, data map[string]any) (map[string]any, error) {
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	tg := config.GetMap(cfg, "telegram")
	if tg == nil {
		tg = map[string]any{}
	}
	if token := strings.TrimSpace(toString(orDefaultAny(data["bot_token"], ""))); token != "" {
		tg["bot_token"] = token
	}
	if strings.TrimSpace(config.GetStr(tg, "bot_token")) == "" {
		return nil, &PanelError{"Telegram Bot Token 不能为空", 400}
	}
	chatID, err := requiredText(data, "chat_id", fmt.Sprintf("%v", tg["chat_id"]), "Telegram Chat ID")
	if err != nil {
		return nil, err
	}
	tg["chat_id"] = chatID
	if tg["timeout_seconds"], err = asInt(data, "timeout_seconds", config.GetInt(tg, "timeout_seconds", 12), 3, 60); err != nil {
		return nil, err
	}
	if tg["retries"], err = asInt(data, "retries", config.GetInt(tg, "retries", 3), 1, 5); err != nil {
		return nil, err
	}
	if tg["control_enabled"], err = asBool(data, "control_enabled", config.GetBool(tg, "control_enabled", true)); err != nil {
		return nil, err
	}
	if raw, present := data["control_admin_ids"]; present {
		ids := []any{}
		switch list := raw.(type) {
		case []any:
			for _, item := range list {
				switch v := item.(type) {
				case float64:
					if v > 0 {
						ids = append(ids, v)
					}
				case string:
					var f float64
					if _, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &f); err != nil || f <= 0 {
						return nil, &PanelError{"管理员用户 ID 必须是正整数", 400}
					}
					ids = append(ids, f)
				default:
					return nil, &PanelError{"管理员用户 ID 必须是正整数", 400}
				}
			}
		case nil:
			tg["control_admin_ids"] = []any{}
		default:
			return nil, &PanelError{"管理员用户 ID 必须是数组", 400}
		}
		if ids != nil {
			tg["control_admin_ids"] = ids
		}
	}
	cfg["telegram"] = tg
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return telegramPayload(tg), nil
}

// testCurrentTelegram 對應 test_current_telegram()：getMe 取得 username 與延遲。
func testCurrentTelegram(paths config.Paths) (map[string]any, error) {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500}
	}
	start := time.Now()
	var lastErr error
	username := ""
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		me, err := notify.CallAPI(cfg, "getMe", nil)
		if err == nil {
			username, _ = me["username"].(string)
			break
		}
		lastErr = err
	}
	if username == "" && lastErr != nil {
		return nil, &PanelError{fmt.Sprintf("Telegram 测试失败: %v", lastErr), 502}
	}
	return map[string]any{
		"username":         username,
		"latency_ms":       float64(time.Since(start).Microseconds()) / 1000.0,
		"latency_attempts": 3,
		"connection":       "直连",
	}, nil
}

func mustLoad(paths config.Paths) map[string]any {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return map[string]any{}
	}
	return cfg
}

func toAnySlice(users []map[string]any) []any {
	out := make([]any, 0, len(users))
	for _, u := range users {
		out = append(out, u)
	}
	return out
}
