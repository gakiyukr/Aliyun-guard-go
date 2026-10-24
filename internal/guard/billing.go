// 帳單查詢快取，對應 Python 版 billing_cache_key / query_instance_bill_cached。
// 快取存放於 state.json 的 billing_cache 欄位，key 為憑證/實例/站點/帳單月份的
// SHA256 指紋；刷新失敗時回退快取值並縮短下次刷新間隔（最長 900 秒）。
package guard

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/xtime"
)

// 帳單查詢鉤子，測試中可替換。
var billQueryFn = aliyun.DescribeInstanceBill

// billingCacheKey 對應 billing_cache_key()：ak\0sk\0instance\0endpoint\0region\0月份 的 SHA256。
func billingCacheKey(user, billing map[string]any, now time.Time) string {
	material := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		trim(user["ak"]), trim(user["sk"]),
		trim(user["instance_id"]),
		trim(billing["endpoint"]), trim(billing["region"]),
		xtime.MonthKey(now),
	)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(material)))
}

func trim(v any) string {
	s, _ := v.(string)
	return s
}

// queryBillCached 對應 query_instance_bill_cached()。
// 回傳（金額, 幣別, 檢查時間, 是否快取, 錯誤）；金額為 nil 表示無可用值。
func queryBillCached(
	user, billing map[string]any,
	state map[string]any,
	cacheSeconds int,
	forceRefresh bool,
	now time.Time,
) (*float64, string, string, bool, error) {
	if now.IsZero() {
		now = time.Now()
	}
	var cache map[string]any
	if state != nil {
		cache, _ = state["billing_cache"].(map[string]any)
		if cache == nil {
			cache = map[string]any{}
			state["billing_cache"] = cache
		}
	}
	key := billingCacheKey(user, billing, now)
	var cached map[string]any
	if cache != nil {
		cached, _ = cache[key].(map[string]any)
	}

	// 快取命中：next_refresh_at 內直接回傳
	if cached != nil && !forceRefresh && cacheSeconds > 0 {
		refreshAt, ok := xtime.ParseStamp(cached["next_refresh_at"])
		if !ok {
			refreshAt, ok = xtime.ParseStamp(cached["checked_at"])
		}
		if ok && now.Before(refreshAt) {
			amount, _ := cached["amount"].(float64)
			currency, _ := cached["currency"].(string)
			checkedAt, _ := cached["checked_at"].(string)
			return &amount, currency, checkedAt, true, nil
		}
	}

	endpoint := fmt.Sprintf("%v", billing["endpoint"])
	instanceID := fmt.Sprintf("%v", user["instance_id"])
	amount, currency, err := billQueryFn(
		trim(user["ak"]), trim(user["sk"]), endpoint, instanceID, now.Format("2006-01"),
	)

	// 刷新失敗但有快取：回退快取值，下次刷新提前（最長 900 秒）
	if err != nil {
		if cached != nil && cached["checked_at"] != nil {
			extend := cacheSeconds
			if extend > 900 {
				extend = 900
			}
			if extend < 0 {
				extend = 0
			}
			cached["next_refresh_at"] = now.Add(time.Duration(extend) * time.Second).Format(xtime.ISOFormat)
			amount, _ := cached["amount"].(float64)
			currency, _ := cached["currency"].(string)
			checkedAt, _ := cached["checked_at"].(string)
			return &amount, currency, checkedAt, true, err
		}
		return nil, "", "", false, err
	}

	checkedAt := now.Format(xtime.ISOFormat)
	if cache != nil {
		currentMonth := now.Format("2006-01")
		cache[key] = map[string]any{
			"amount":          amount,
			"currency":        currency,
			"checked_at":      checkedAt,
			"next_refresh_at": now.Add(time.Duration(cacheSeconds) * time.Second).Format(xtime.ISOFormat),
			"instance_id":     instanceID,
			"billing_cycle":   currentMonth,
		}
		pruneBillingCache(cache, currentMonth)
	}
	return &amount, currency, checkedAt, false, nil
}

// pruneBillingCache 清理過期週期與超量條目，對應 query_instance_bill_cached 尾段的清理：
// 移除 billing_cycle 非當月（且非空）的條目；超過 1024 筆時僅保留最新的 512 筆。
func pruneBillingCache(cache map[string]any, currentMonth string) {
	for k, v := range cache {
		m, ok := v.(map[string]any)
		if !ok {
			delete(cache, k)
			continue
		}
		cycle, _ := m["billing_cycle"].(string)
		if cycle != "" && cycle != currentMonth {
			delete(cache, k)
		}
	}
	if len(cache) <= 1024 {
		return
	}
	type entry struct {
		key       string
		checkedAt string
	}
	entries := make([]entry, 0, len(cache))
	for k, v := range cache {
		m, _ := v.(map[string]any)
		checkedAt, _ := m["checked_at"].(string)
		entries = append(entries, entry{k, checkedAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].checkedAt > entries[j].checkedAt
	})
	keep := map[string]bool{}
	for _, e := range entries[:512] {
		keep[e.key] = true
	}
	for k := range cache {
		if !keep[k] {
			delete(cache, k)
		}
	}
}
