package aliyun

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeECS 啟動假 ECS 並注入 NewDiscoveryClient stub；handler 依 Action 回應。
func fakeECS(t *testing.T, handler func(action string, query url.Values) (int, string)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		code, body := handler(action, r.URL.Query())
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	orig := NewDiscoveryClient
	NewDiscoveryClient = func(ak, sk, domain, version string) *Client {
		c := New(ak, sk, domain, version)
		// 覆寫 transport 不夠——直接改 Domain 為測試伺服器位址
		c.Domain = strings.TrimPrefix(srv.URL, "http://")
		c.Scheme = "http"
		c.HTTP = srv.Client()
		return c
	}
	t.Cleanup(func() { NewDiscoveryClient = orig; srv.Close() })
}

// TestDiscoverRegions 驗證 Region 去重與空值過濾。
func TestDiscoverRegions(t *testing.T) {
	fakeECS(t, func(action string, q url.Values) (int, string) {
		if action != "DescribeRegions" {
			t.Errorf("Action 不符: %s", action)
			return 400, `{"Code":"InvalidAction","Message":"x"}`
		}
		return 200, `{"Regions":{"Region":[
			{"RegionId":"cn-hangzhou"},{"RegionId":"cn-hangzhou"},
			{"RegionId":" "},{"RegionId":"us-east-1"}]}}`
	})
	regions, err := DiscoverRegions("ak", "sk")
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0] != "cn-hangzhou" || regions[1] != "us-east-1" {
		t.Errorf("Region 列表不符: %v", regions)
	}
}

// TestDiscoverRegionsEmptyAKSK 空憑證逐字錯誤。
func TestDiscoverRegionsEmptyAKSK(t *testing.T) {
	_, err := DiscoverRegions("", "  ")
	if err == nil || err.Error() != "AccessKey ID 和 AccessKey Secret 不能为空" {
		t.Errorf("錯誤不符: %v", err)
	}
}

// TestDiscoverInstancesPaginationAndTags 驗證分頁、tag 過濾、public_ip 取首個。
func TestDiscoverInstancesPaginationAndTags(t *testing.T) {
	fakeECS(t, func(action string, q url.Values) (int, string) {
		if q.Get("PageSize") != "100" {
			t.Errorf("PageSize 應為 100: %s", q.Encode())
		}
		if q.Get("PageNumber") == "1" {
			return 200, `{"TotalCount":150,"Instances":{"Instance":[
				{"InstanceId":"i-1","InstanceName":"web-1","Status":"Running",
				 "ZoneId":"cn-hangzhou-a","InstanceType":"ecs.g6.large",
				 "PublicIpAddress":{"IpAddress":["1.2.3.4","5.6.7.8"]},
				 "Tags":{"Tag":[{"TagKey":"env","TagValue":"prod"},{"TagKey":"x","TagValue":""}]}},
				{"InstanceId":"i-2","Status":"Stopped",
				 "Tags":{"Tag":[{"TagKey":"env","TagValue":"dev"}]}}]}}`
		}
		// 第二頁：剩 50 台（無 tag 欄位）
		return 200, `{"TotalCount":150,"Instances":{"Instance":[
			{"InstanceId":"i-150","InstanceName":"web-150"}]}}`
	})
	result, err := DiscoverInstances("ak", "sk", []string{"cn-hangzhou"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 3 {
		t.Fatalf("應有 3 台實例: %d", len(result.Instances))
	}
	first := result.Instances[0]
	if first.PublicIP != "1.2.3.4" {
		t.Errorf("public_ip 應取首個: %q", first.PublicIP)
	}
	if first.Name != "web-1" || first.Status != "Running" || first.ZoneID != "cn-hangzhou-a" {
		t.Errorf("欄位不符: %+v", first)
	}
	if first.Tags["env"] != "prod" {
		t.Errorf("tags 不符: %v", first.Tags)
	}
	if _, ok := first.Tags["x"]; !ok {
		t.Errorf("空值 tag 應保留: %v", first.Tags)
	}
	third := result.Instances[2]
	if third.Name != "web-150" || third.Status != "Unknown" {
		t.Errorf("預設名稱/狀態不符: %+v", third)
	}
}

// TestDiscoverInstancesTagFilter tag_key/tag_value 客戶端過濾。
func TestDiscoverInstancesTagFilter(t *testing.T) {
	fakeECS(t, func(action string, q url.Values) (int, string) {
		return 200, `{"TotalCount":2,"Instances":{"Instance":[
			{"InstanceId":"i-1","Tags":{"Tag":[{"TagKey":"env","TagValue":"prod"}]}},
			{"InstanceId":"i-2","Tags":{"Tag":[{"TagKey":"env","TagValue":"dev"}]}}]}}`
	})
	result, err := DiscoverInstances("ak", "sk", []string{"cn-hangzhou"}, "env", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 1 || result.Instances[0].InstanceID != "i-1" {
		t.Errorf("tag 過濾不符: %+v", result.Instances)
	}
}

// TestDiscoverInstancesRegionErrorIsolated 單一 region 失敗不中斷其他 region。
func TestDiscoverInstancesRegionErrorIsolated(t *testing.T) {
	fakeECS(t, func(action string, q url.Values) (int, string) {
		if strings.Contains(q.Get("RegionId"), "bad") || q.Get("RegionId") == "" {
			return 403, `{"Code":"AccessDenied","Message":"denied for ak123"}`
		}
		return 200, `{"TotalCount":1,"Instances":{"Instance":[{"InstanceId":"i-ok"}]}}`
	})
	result, err := DiscoverInstances("ak123", "sk", []string{"bad-region", "cn-hangzhou"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 1 || result.Instances[0].InstanceID != "i-ok" {
		t.Errorf("好 region 應正常收錄: %+v", result.Instances)
	}
	if len(result.Errors) != 1 || result.Errors[0].Region != "bad-region" {
		t.Fatalf("壞 region 應記入 errors: %+v", result.Errors)
	}
	// CompactError 脫敏：sk 部分不應出現（ak 已被 "***" 取代）
	if strings.Contains(result.Errors[0].Error, "ak123") {
		t.Errorf("錯誤應脫敏: %s", result.Errors[0].Error)
	}
}

// TestDiscoverInstancesLimits 空 region 清單與超過 50 個 region 逐字錯誤。
func TestDiscoverInstancesLimits(t *testing.T) {
	if _, err := DiscoverInstances("ak", "sk", []string{" ", ""}, "", ""); err == nil ||
		err.Error() != "至少需要一个 Region ID" {
		t.Errorf("錯誤不符: %v", err)
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("r%02d", i)
	}
	if _, err := DiscoverInstances("ak", "sk", many, "", ""); err == nil ||
		err.Error() != "一次最多扫描 50 个 Region" {
		t.Errorf("錯誤不符: %v", err)
	}
}

// TestCompactErrorRedaction 驗證 URL 參數遮蔽與 secret 脫敏。
func TestCompactErrorRedaction(t *testing.T) {
	err := fmt.Errorf("GET https://ecs.aliyuncs.com/?AccessKeyId=SECRET&Signature=abc failed")
	got := CompactError(err, []string{"SECRET"})
	if strings.Contains(got, "SECRET") || strings.Contains(got, "Signature=abc") {
		t.Errorf("應遮蔽參數與 secret: %s", got)
	}
	if !strings.Contains(got, "https://ecs.aliyuncs.com/?[请求参数已隐藏]") {
		t.Errorf("URL 應保留路徑並隱藏參數: %s", got)
	}
	long := errors.New(strings.Repeat("x", 600))
	if len(CompactError(long, nil)) != 500 {
		t.Errorf("應截 500 字")
	}
}
