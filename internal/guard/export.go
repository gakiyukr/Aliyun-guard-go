// 供其他套件（tgbot/web）使用的 guard 公開入口：鎖、檢測輪、查詢。
package guard

import (
	"fmt"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/state"
	"aliyun-guard-go/internal/xtime"
)

// AcquireCycleLockPublic 對外開放週期鎖（Bot/面板與檢測輪互斥）。
func AcquireCycleLockPublic(paths config.Paths) (func(), bool) {
	return acquireCycleLock(paths)
}

// ConfigLock / ConfigUnlock 對外開放配置交易鎖（對應 config_lock()）。
func ConfigLock()   { configMu.Lock() }
func ConfigUnlock() { configMu.Unlock() }

// RunCyclePublic 以磁碟上的 config/state 執行一輪檢測（供網路面板背景任務）。
func RunCyclePublic(paths config.Paths, dryRun, noNotify bool) {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		Logger.Errorf("RunCyclePublic: 配置读取失败: %v", err)
		return
	}
	st, err := state.Load(paths.State)
	if err != nil {
		st = map[string]any{}
	}
	RunCycle(paths, cfg, st, dryRun, noNotify)
}

// RefreshBillingCachePublic 強制刷新全部實例的帳單快取並更新 state，
// 對應 refresh_billing_cache()；回傳結果 dict（ok/at/refreshed/failed/items）。
func RefreshBillingCachePublic(paths config.Paths) map[string]any {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return map[string]any{"ok": false, "error": fmt.Sprintf("配置读取失败: %v", err)}
	}
	st, err := state.Load(paths.State)
	if err != nil {
		st = map[string]any{}
	}
	cacheSeconds := config.GetInt(cfg, "billing_cache_seconds", 3600)
	now := time.Now()
	results := []any{}
	for _, user := range config.Users(cfg) {
		billing := config.BillingConfig(user)
		instanceID := config.GetStr(user, "instance_id")
		name := config.GetStr(user, "name")
		if name == "" {
			name = instanceID
		}
		item := map[string]any{
			"name": name, "instance_id": instanceID,
			"enabled": config.GetBool(billing, "enabled", true),
			"ok":      true, "amount": nil, "currency": nil, "checked_at": nil,
			"from_cache": false, "error": nil, "skipped": false,
		}
		if !item["enabled"].(bool) || config.GetBool(user, "paused", false) {
			item["skipped"] = true
			results = append(results, item)
			continue
		}
		amount, currency, checkedAt, fromCache, refreshErr := queryBillCached(
			user, billing, st, cacheSeconds, true, now)
		if amount != nil {
			item["amount"] = *amount
			item["currency"] = currency
			item["checked_at"] = checkedAt
			item["from_cache"] = fromCache
		}
		if refreshErr != nil {
			item["ok"] = false
			item["error"] = refreshErr.Error()
		}
		instances, _ := st["instances"].(map[string]any)
		if instances == nil {
			instances = map[string]any{}
			st["instances"] = instances
		}
		current, _ := instances[instanceID].(map[string]any)
		if current == nil {
			current = map[string]any{}
		}
		current["name"] = name
		if amount != nil {
			current["bill_amount"] = *amount
			current["bill_currency"] = currency
			current["bill_checked_at"] = checkedAt
			current["bill_from_cache"] = fromCache
		}
		switch {
		case item["error"] != nil && amount != nil:
			current["bill_error"] = fmt.Sprintf("BSS 账单刷新失败，继续使用缓存: %v", item["error"])
		case item["error"] != nil:
			current["bill_error"] = fmt.Sprintf("BSS 账单查询失败: %v", item["error"])
		default:
			current["bill_error"] = nil
		}
		instances[instanceID] = current
		results = append(results, item)
	}
	_ = state.Save(paths.State, st)
	failed := 0
	refreshed := 0
	for _, raw := range results {
		item := raw.(map[string]any)
		if item["ok"] != true {
			failed++
		} else if item["skipped"] != true {
			refreshed++
		}
	}
	return map[string]any{
		"ok":        failed == 0,
		"at":        now.Format(xtime.ISOFormat),
		"refreshed": refreshed,
		"failed":    failed,
		"items":     results,
	}
}

// QueryInstanceStatusPublic 查詢單一實例狀態（user 為 config 條目）。
func QueryInstanceStatusPublic(user map[string]any) (string, error) {
	return aliyun.QueryInstanceStatus(
		config.GetStr(user, "ak"), config.GetStr(user, "sk"),
		config.GetStr(user, "region"), config.GetStr(user, "instance_id"))
}

// QueryTrafficPublic 查詢帳號級 CDT 流量（GB）。
func QueryTrafficPublic(user map[string]any) (float64, error) {
	return aliyun.ListCdtInternetTraffic(config.GetStr(user, "ak"), config.GetStr(user, "sk"))
}
