// 面板 API 載入配置/狀態的讀取端 payload 組裝，對應 web_panel.py 的
// dashboard_payload / management_payload / logs_payload 與 web_actions.py 的
// instance/telegram payload（已裁剪 s3_backup、rollback、節點代理欄位）。
package web

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/guard"
	"aliyun-guard-go/internal/state"
	"aliyun-guard-go/internal/xtime"
)

const appVersion = "1.6.15-go"

// getWebConfig 對應 get_web_config()：web_panel 區段 + 預設值正規化。
func getWebConfig(cfg map[string]any) map[string]any {
	wp := config.GetMap(cfg, "web_panel")
	result := map[string]any{
		"enabled":       false,
		"host":          "127.0.0.1",
		"port":          8765,
		"username":      "admin",
		"password_hash": "",
		"cookie_secure": false,
	}
	for key, value := range wp {
		result[key] = value
	}
	result["enabled"] = config.GetBool(wp, "enabled", false)
	host := strings.TrimSpace(config.GetStr(wp, "host"))
	if host == "" {
		host = "127.0.0.1"
	}
	result["host"] = host
	result["username"] = orEmptyDefault(strings.TrimSpace(config.GetStr(wp, "username")), "admin")
	result["password_hash"] = config.GetStr(wp, "password_hash")
	result["cookie_secure"] = config.GetBool(wp, "cookie_secure", false)
	result["port"] = config.GetInt(wp, "port", 8765)
	return result
}

func orEmptyDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// validateWebConfig 對應 validate_web_config()。
func validateWebConfig(cfg map[string]any) error {
	web := getWebConfig(cfg)
	if web["host"] != "127.0.0.1" && web["host"] != "0.0.0.0" {
		return fmt.Errorf("网页面板监听地址只能是 127.0.0.1 或 0.0.0.0")
	}
	if web["port"].(int) < 1024 || web["port"].(int) > 65535 {
		return fmt.Errorf("网页面板端口必须在 1024 到 65535 之间")
	}
	username := web["username"].(string)
	if username == "" || len(username) > 64 {
		return fmt.Errorf("网页面板用户名不能为空且不能超过 64 个字符")
	}
	if web["enabled"].(bool) && !strings.HasPrefix(web["password_hash"].(string), "pbkdf2_sha256$") {
		return fmt.Errorf("网页面板尚未设置有效登录密码")
	}
	return nil
}

func safeInstances(st map[string]any) map[string]any {
	instances, _ := st["instances"].(map[string]any)
	if instances == nil {
		return map[string]any{}
	}
	return instances
}

// dashboardPayload 對應 dashboard_payload()：實例卡片、流量曲線取樣、
// 服務狀態、設定與下次 CDT 月結時間。
func dashboardPayload(paths config.Paths, job map[string]any) (map[string]any, error) {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(paths.State)
	if err != nil {
		st = map[string]any{}
	}
	now := time.Now()
	states := safeInstances(st)
	history, _ := st["history"].([]any)
	users := []any{}
	for index, user := range config.Users(cfg) {
		instanceID := config.GetStr(user, "instance_id")
		current, _ := states[instanceID].(map[string]any)
		if current == nil {
			current = map[string]any{}
		}
		billing := config.BillingConfig(user)
		schedule := guard.GetScheduleConfig(user)
		enabled := schedule["enabled"] == true
		var nextAt, nextAction any
		var target any
		if enabled {
			target = guard.ScheduleTarget(user, now)
			if eventAt, action, ok := guard.NextScheduleEvent(user, now); ok {
				nextAt = eventAt.Truncate(time.Minute).Format("2006-01-02T15:04")
				nextAction = action
			}
		}
		points := []any{}
		start := len(history) - 96
		if start < 0 {
			start = 0
		}
		for _, sample := range history[start:] {
			entry, ok := sample.(map[string]any)
			if !ok {
				continue
			}
			values, _ := entry["instances"].(map[string]any)
			value, _ := values[instanceID].(map[string]any)
			if value == nil || value["traffic_gb"] == nil {
				continue
			}
			points = append(points, map[string]any{
				"at":               entry["at"],
				"value":            value["traffic_gb"],
				"status":           orNull(value["status_after"], value["status"]),
				"status_before":    value["status_before"],
				"action":           orDefaultAny(value["action"], "none"),
				"action_performed": value["action_performed"] == true,
				"message":          orDefaultAny(value["message"], ""),
				"level":            orDefaultAny(value["level"], "unknown"),
			})
		}
		var traffic any
		if v, ok := current["traffic_gb"].(float64); ok {
			traffic = v
		}
		limit := config.GetFloat(user, "traffic_limit_gb", 0)
		var percent any
		if traffic != nil && limit > 0 {
			percent = math.Round(traffic.(float64)/limit*10000) / 100
		}
		checkedAt := current["checked_at"]
		users = append(users, map[string]any{
			"index":                index,
			"name":                 orDefaultAny(config.GetStr(user, "name"), instanceID),
			"instance_id":          instanceID,
			"region":               config.GetStr(user, "region"),
			"paused":               config.GetBool(user, "paused", false),
			"actions_enabled":      config.GetBool(user, "actions_enabled", true),
			"instance_log_enabled": config.GetBool(user, "instance_log_enabled", false),
			"traffic_gb":           traffic,
			"traffic_limit_gb":     limit,
			"traffic_percent":      percent,
			"status":               orDefaultAny(current["status_after"], "Unknown"),
			"level":                orDefaultAny(current["level"], "unknown"),
			"message":              orDefaultAny(current["message"], "尚未完成检测"),
			"checked_at":           checkedAt,
			"bill_amount":          current["bill_amount"],
			"bill_currency":        current["bill_currency"],
			"bill_error":           current["bill_error"],
			"bill_checked_at":      current["bill_checked_at"],
			"bill_from_cache":      current["bill_from_cache"] == true,
			"bill_symbol":          config.GetStr(billing, "currency_symbol"),
			"billing_enabled":      config.GetBool(billing, "enabled", true),
			"schedule": map[string]any{
				"enabled":     enabled,
				"start_time":  schedule["start_time"],
				"stop_time":   schedule["stop_time"],
				"timezone":    schedule["timezone"],
				"target":      target,
				"next_at":     nextAt,
				"next_action": nextAction,
			},
			"history": points,
		})
	}
	finished, _ := st["last_cycle_finished_at"].(string)
	stale := true
	if finished != "" {
		if parsed, ok := xtime.ParseStamp(finished); ok {
			stale = now.Sub(parsed) > time.Duration(maxInt(180, config.GetInt(cfg, "interval_seconds", 300)*2))*time.Second
		}
	}
	web := getWebConfig(cfg)
	nextReset := xtime.NextCDTReset(now)
	return map[string]any{
		"version": appVersion,
		"now":     now.Format(xtime.ISOFormat),
		"cdt_reset": map[string]any{
			"next_at":  nextReset.Format(xtime.ISOFormat),
			"timezone": "UTC+8",
		},
		"users": users,
		"service": map[string]any{
			"cycle_count":      config.GetInt(st, "cycle_count", 0),
			"last_finished_at": finished,
			"last_ok":          st["last_cycle_ok"] == true,
			"last_error_count": config.GetInt(st, "last_cycle_error_count", 0),
			"telegram_error":   st["telegram_error"],
			"stale":            stale,
		},
		"settings": map[string]any{
			"interval_seconds":  config.GetInt(cfg, "interval_seconds", 300),
			"notification_mode": orEmptyDefault(config.GetStr(cfg, "notification_mode"), "always"),
			"force_ipv4":        config.GetBool(cfg, "force_ipv4", true),
			"web_host":          web["host"],
			"web_port":          web["port"],
		},
		"job": job,
	}, nil
}

// instancePayload 對應 _instance_payload()（不含 AK/SK 明文）。
func instancePayload(user map[string]any, index int) map[string]any {
	schedule := guard.GetScheduleConfig(user)
	return map[string]any{
		"index":                 index,
		"name":                  config.GetStr(user, "name"),
		"access_key_configured": strings.TrimSpace(config.GetStr(user, "ak")) != "",
		"secret_key_configured": strings.TrimSpace(config.GetStr(user, "sk")) != "",
		"region":                config.GetStr(user, "region"),
		"instance_id":           config.GetStr(user, "instance_id"),
		"traffic_limit_gb":      config.GetFloat(user, "traffic_limit_gb", 0),
		"actions_enabled":       config.GetBool(user, "actions_enabled", true),
		"instance_log_enabled":  config.GetBool(user, "instance_log_enabled", false),
		"paused":                config.GetBool(user, "paused", false),
		"billing":               billingPayload(user),
		"schedule": map[string]any{
			"enabled":    schedule["enabled"],
			"start_time": schedule["start_time"],
			"stop_time":  schedule["stop_time"],
			"timezone":   schedule["timezone"],
		},
	}
}

func billingPayload(user map[string]any) map[string]any {
	billing := config.BillingConfig(user)
	return map[string]any{
		"enabled":         config.GetBool(billing, "enabled", true),
		"site":            orEmptyDefault(config.GetStr(billing, "site"), "china"),
		"endpoint":        config.GetStr(billing, "endpoint"),
		"region":          config.GetStr(billing, "region"),
		"currency_code":   config.GetStr(billing, "currency_code"),
		"currency_symbol": config.GetStr(billing, "currency_symbol"),
	}
}

// telegramPayload 對應 telegram_payload()（節點代理欄位以停用值保留，
// 前端沿用原版 HTML 不需改動）。
func telegramPayload(tg map[string]any) map[string]any {
	explicit := []any{}
	for _, item := range config.GetSlice(tg, "control_admin_ids") {
		if f, ok := item.(float64); ok {
			explicit = append(explicit, f)
		}
	}
	effective := tgbotEffectiveAdmins(tg)
	usesChat := len(effective) > 0 && len(explicit) == 0
	return map[string]any{
		"bot_token_configured":            strings.TrimSpace(config.GetStr(tg, "bot_token")) != "",
		"chat_id":                         fmt.Sprintf("%v", tg["chat_id"]),
		"timeout_seconds":                 config.GetInt(tg, "timeout_seconds", 12),
		"retries":                         config.GetInt(tg, "retries", 3),
		"control_enabled":                 config.GetBool(tg, "control_enabled", true),
		"control_admin_ids":               explicit,
		"control_effective_admin_ids":     effective,
		"control_uses_chat_id":            usesChat,
		"connection_mode":                 "direct",
		"connection_label":                "直连",
		"connection_description":          "直连",
		"proxy_configured":                false,
		"api_base_configured":             strings.TrimSpace(config.GetStr(tg, "api_base_url")) != "",
		"subscription_configured":         false,
		"subscription_last_refresh_epoch": 0,
		"subscription_refresh_seconds":    0,
		"nodes":                           []any{},
	}
}

func tgbotEffectiveAdmins(tg map[string]any) []any {
	seen := map[float64]bool{}
	out := []any{}
	for _, item := range config.GetSlice(tg, "control_admin_ids") {
		if f, ok := item.(float64); ok && f > 0 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		if chatID, ok := tg["chat_id"].(float64); ok && chatID > 0 {
			out = append(out, chatID)
		}
	}
	return out
}

// managementPayload 對應 management_payload()（rollback_snapshots 維持空
// 佔位——回滾快照模組已裁決放棄）。
func managementPayload(paths config.Paths, backend string) (map[string]any, error) {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	tg := config.GetMap(cfg, "telegram")
	wp := config.GetMap(cfg, "web_panel")
	watchdog := config.GetMap(cfg, "watchdog")
	instances := []any{}
	for index, user := range config.Users(cfg) {
		instances = append(instances, instancePayload(user, index))
	}
	return map[string]any{
		"instances": instances,
		"telegram":  telegramPayload(tg),
		"settings": map[string]any{
			"interval_seconds":       config.GetInt(cfg, "interval_seconds", 300),
			"billing_cache_seconds":  config.GetInt(cfg, "billing_cache_seconds", 3600),
			"notification_mode":      orEmptyDefault(config.GetStr(cfg, "notification_mode"), "always"),
			"force_ipv4":             config.GetBool(cfg, "force_ipv4", true),
			"notify_on_daemon_start": config.GetBool(cfg, "notify_on_daemon_start", false),
			"start_wait_seconds":     config.GetInt(cfg, "start_wait_seconds", 90),
			"stop_wait_seconds":      config.GetInt(cfg, "stop_wait_seconds", 45),
			"start_poll_seconds":     config.GetInt(cfg, "start_poll_seconds", 5),
			"watchdog": map[string]any{
				"enabled":           config.GetBool(watchdog, "enabled", true),
				"timeout_seconds":   config.GetInt(watchdog, "timeout_seconds", 600),
				"failure_threshold": config.GetInt(watchdog, "failure_threshold", 2),
			},
		},
		"web": map[string]any{
			"enabled":             config.GetBool(wp, "enabled", false),
			"host":                orEmptyDefault(config.GetStr(wp, "host"), "127.0.0.1"),
			"port":                config.GetInt(wp, "port", 8765),
			"username":            orEmptyDefault(config.GetStr(wp, "username"), "admin"),
			"password_configured": strings.TrimSpace(config.GetStr(wp, "password_hash")) != "",
		},
		"backend":            backend,
		"s3_backup":          s3BackupPayload(orMap(cfg["s3_backup"]), paths),
		"rollback_snapshots": []any{},
		"version":            appVersion,
		"local_ip":           detectPrimaryIPv4(),
		"browser_url":        fmt.Sprintf("http://%s:%d", detectPrimaryIPv4(), config.GetInt(wp, "port", 8765)),
	}, nil
}

// logsPayload 對應 logs_payload()：系統總日誌（per-instance JSONL 屬 M4）。
func logsPayload(paths config.Paths, limit int, instanceIndex string) (map[string]any, error) {
	if limit < 20 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if instanceIndex == "" || instanceIndex == "system" {
		lines, err := readRecentLog(filepath.Dir(paths.Config), "logs/guard.log", limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"lines": lines, "source": "system", "name": "系统总日志",
			"instance_id": nil, "index": nil, "enabled": true, "toggle_available": false,
		}, nil
	}
	index := 0
	if _, err := fmt.Sscanf(instanceIndex, "%d", &index); err != nil {
		return nil, &PanelError{"实例日志序号无效", 400}
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	users := config.Users(cfg)
	if index < 0 || index >= len(users) {
		return nil, &PanelError{"实例不存在", 404}
	}
	user := users[index]
	// 讀取 per-instance 輪轉日誌（與守護行程 logs/instances/<id>-<hash>.log 對應）
	lines := readRecentLogAt(guard.InstanceLogPath(paths, user), limit)
	return map[string]any{
		"lines": lines, "source": "instance",
		"name":        orDefaultAny(config.GetStr(user, "name"), config.GetStr(user, "instance_id")),
		"instance_id": config.GetStr(user, "instance_id"),
		"index":       index, "enabled": config.GetBool(user, "instance_log_enabled", false),
		"toggle_available": true,
	}, nil
}

func readRecentLog(home, relPath string, limit int) ([]string, error) {
	return readRecentLogAt(filepath.Join(home, relPath), limit), nil
}

func readRecentLogAt(path string, limit int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{} // 檔案不存在視為空日誌
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// detectPrimaryIPv4 對應 detect_primary_ipv4()：以 UDP 連外探測本機位址。
func detectPrimaryIPv4() string {
	if env := strings.TrimSpace(os.Getenv("ALIYUN_GUARD_PUBLIC_IP")); env != "" {
		return env
	}
	for _, dest := range []string{"1.1.1.1:80", "8.8.8.8:80"} {
		conn, err := net.Dial("udp4", dest)
		if err != nil {
			continue
		}
		addr := conn.LocalAddr().(*net.UDPAddr).IP.String()
		_ = conn.Close()
		if ip := net.ParseIP(addr); ip != nil && !ip.IsLoopback() && ip.To4() != nil {
			return addr
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var candidates []string
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			ip := ipnet.IP
			if ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				candidates = append(candidates, ip.String())
			}
		}
	}
	sort.Strings(candidates)
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

func orNull(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func orDefaultAny(v any, fallback any) any {
	if s, ok := v.(string); ok && s == "" {
		return fallback
	}
	if v == nil {
		return fallback
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
