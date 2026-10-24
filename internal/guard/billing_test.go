// 帳單快取測試：命中、強制刷新、失敗回退與提前重試間隔。
package guard

import (
	"errors"
	"testing"
	"time"
)

func testUser() map[string]any {
	return map[string]any{"ak": "a", "sk": "s", "instance_id": "i-1"}
}

func TestQueryBillCachedHitAndRefresh(t *testing.T) {
	orig := billQueryFn
	defer func() { billQueryFn = orig }()
	calls := 0
	billQueryFn = func(ak, sk, endpoint, instanceID, cycle string) (float64, string, error) {
		calls++
		return 1.5, "USD", nil
	}
	st := map[string]any{}
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

	// 首查：直查
	amount, currency, _, fromCache, err := queryBillCached(testUser(), map[string]any{"endpoint": "business.aliyuncs.com"}, st, 3600, false, now)
	if err != nil || fromCache || *amount != 1.5 || currency != "USD" || calls != 1 {
		t.Fatalf("首查應直查: %v %v %v calls=%d", amount, fromCache, err, calls)
	}
	// 快取期內：命中
	_, _, _, fromCache, _ = queryBillCached(testUser(), map[string]any{"endpoint": "business.aliyuncs.com"}, st, 3600, false, now.Add(time.Minute))
	if !fromCache || calls != 1 {
		t.Fatalf("快取期內應命中且不直查: fromCache=%v calls=%d", fromCache, calls)
	}
	// 強制刷新：直查
	_, _, _, fromCache, _ = queryBillCached(testUser(), map[string]any{"endpoint": "business.aliyuncs.com"}, st, 3600, true, now.Add(time.Minute))
	if fromCache || calls != 2 {
		t.Fatalf("強制刷新應直查: fromCache=%v calls=%d", fromCache, calls)
	}
}

func TestQueryBillCachedFailureFallback(t *testing.T) {
	orig := billQueryFn
	defer func() { billQueryFn = orig }()
	// 預填成功快取
	billQueryFn = func(ak, sk, endpoint, instanceID, cycle string) (float64, string, error) {
		return 2.0, "USD", nil
	}
	st := map[string]any{}
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	queryBillCached(testUser(), map[string]any{"endpoint": "business.aliyuncs.com"}, st, 3600, false, now)

	// 刷新失敗 → 回退快取並帶錯誤
	billQueryFn = func(ak, sk, endpoint, instanceID, cycle string) (float64, string, error) {
		return 0, "", errors.New("boom")
	}
	amount, _, _, fromCache, err := queryBillCached(testUser(), map[string]any{"endpoint": "business.aliyuncs.com"}, st, 3600, false, now.Add(2*time.Hour))
	if err == nil || !fromCache || amount == nil || *amount != 2.0 {
		t.Fatalf("失敗應回退快取並帶錯誤: %v %v %v", amount, fromCache, err)
	}
	// 回退後 next_refresh_at 應提前（≤900 秒）：立即再查仍命中快取
	_, _, _, fromCache, _ = queryBillCached(testUser(), map[string]any{"endpoint": "business.aliyuncs.com"}, st, 3600, false, now.Add(2*time.Hour+time.Minute))
	if !fromCache {
		t.Fatal("失敗回退後短間隔內應繼續用快取")
	}
}

func TestPruneBillingCache(t *testing.T) {
	cache := map[string]any{
		"old": map[string]any{"billing_cycle": "2026-08", "checked_at": "2026-08-01T00:00:00+00:00"},
		"cur": map[string]any{"billing_cycle": "2026-09", "checked_at": "2026-09-01T00:00:00+00:00"},
	}
	pruneBillingCache(cache, "2026-09")
	if _, exists := cache["old"]; exists {
		t.Fatal("過期週期條目應被清理")
	}
	if _, exists := cache["cur"]; !exists {
		t.Fatal("當月條目應保留")
	}
}
