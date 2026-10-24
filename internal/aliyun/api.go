// 本檔封裝守護工具用到的所有阿里雲 API 呼叫，參數與 Python 版逐一對應：
//   - ECS  DescribeInstances / StartInstance / StopInstance（2014-05-26）
//   - CDT  ListCdtInternetTraffic（2021-08-13，帳號級流量，固定 cn-hangzhou 簽名）
//   - BSS  DescribeInstanceBill（2017-12-14，中國站/國際站不同 endpoint）
package aliyun

import (
	"encoding/json"
	"fmt"
	"time"
)

// ecsClient 建立 ECS 客戶端（endpoint 依實例所在 region）。
func ecsClient(ak, sk, region string) *Client {
	return New(ak, sk, "ecs."+region+".aliyuncs.com", "2014-05-26")
}

// DescribeInstances 查詢一組實例的狀態，回傳 instanceID → Status 對照。
// 對應 Python 版 query_instance_status / query_instance_statuses。
func DescribeInstances(ak, sk, region string, instanceIDs []string) (map[string]string, error) {
	ids, err := json.Marshal(instanceIDs)
	if err != nil {
		return nil, err
	}
	params := map[string]string{
		"RegionId":    region,
		"InstanceIds": string(ids),
	}
	if len(instanceIDs) > 1 {
		params["PageSize"] = fmt.Sprintf("%d", len(instanceIDs))
	}
	data, err := ecsClient(ak, sk, region).Call("DescribeInstances", params)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	instances := nestedSlice(data, "Instances", "Instance")
	for _, item := range instances {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["InstanceId"].(string)
		status, _ := m["Status"].(string)
		if id != "" {
			out[id] = status
		}
	}
	return out, nil
}

// QueryInstanceStatus 查詢單一實例狀態；找不到實例時回傳錯誤（與 Python 版一致）。
func QueryInstanceStatus(ak, sk, region, instanceID string) (string, error) {
	statuses, err := DescribeInstances(ak, sk, region, []string{instanceID})
	if err != nil {
		return "", err
	}
	status, ok := statuses[instanceID]
	if !ok {
		return "", fmt.Errorf("区域 %s 中未找到实例 %s", region, instanceID)
	}
	if status == "" {
		return "Unknown", nil
	}
	return status, nil
}

// StartInstance 提交開機請求。
func StartInstance(ak, sk, region, instanceID string) error {
	_, err := ecsClient(ak, sk, region).Call("StartInstance", map[string]string{"InstanceId": instanceID})
	return err
}

// StopInstance 提交關機請求。
func StopInstance(ak, sk, region, instanceID string) error {
	_, err := ecsClient(ak, sk, region).Call("StopInstance", map[string]string{"InstanceId": instanceID})
	return err
}

// LogWarning 為套件級警告日誌鉤子：本套件不得反向依賴 guard 取得分級
// 日誌器，由主程式（cmd/guard）注入 guard.Logger.Warningf；未注入則靜默。
var LogWarning = func(format string, args ...any) {}

// ListCdtInternetTraffic 查詢帳號級 CDT 公網流量，回傳合計 GB。
// 對應 query_cdt_traffic_gb()：累加 TrafficDetails[].Traffic（位元組）後除以 2^30。
// 可重試的傳輸錯誤退避重試（1s、2s），並經 LogWarning 輸出與上游逐字一致的警告。
func ListCdtInternetTraffic(ak, sk string) (float64, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second) // 退避 1s、2s，與 Python 版一致
		}
		data, err := New(ak, sk, "cdt.aliyuncs.com", "2021-08-13").Call("ListCdtInternetTraffic", nil)
		if err != nil {
			if attempt >= 2 || !IsRetryable(err) {
				return 0, err
			}
			// 與上游措辭逐字一致：delay 為下次退避秒數，「第 N 次」為即將進行的重試序號。
			LogWarning("[CDT] 流量查询网络失败，将在 %d 秒后重试（第 %d/%d 次）: %v",
				attempt+1, attempt+2, 3, err)
			lastErr = err
			continue
		}
		var totalBytes float64
		for _, item := range nestedSlice(data, "TrafficDetails") {
			if m, ok := item.(map[string]any); ok {
				totalBytes += jsonFloat(m, "Traffic")
			}
		}
		return totalBytes / (1 << 30), nil
	}
	return 0, lastErr
}

// DescribeInstanceBill 查詢單實例當月帳單，回傳（金額, 幣別）。
// endpoint 依帳單站點：中國站 business.aliyuncs.com、國際站 business.ap-southeast-1.aliyuncs.com。
func DescribeInstanceBill(ak, sk, endpoint, instanceID, billingCycle string) (float64, string, error) {
	data, err := New(ak, sk, endpoint, "2017-12-14").Call("DescribeInstanceBill", map[string]string{
		"BillingCycle": billingCycle,
		"InstanceID":   instanceID,
		"ProductCode":  "ecs",
		"PageNum":      "1",
		"PageSize":     "300",
	})
	if err != nil {
		return 0, "", err
	}
	items := nestedSlice(data, "Data", "Items")
	var amount float64
	currency := ""
	if dataMap, ok := data["Data"].(map[string]any); ok {
		currency, _ = dataMap["Currency"].(string)
	}
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			amount += jsonFloat(m, "PretaxAmount")
			if currency == "" {
				currency, _ = m["Currency"].(string)
			}
		}
	}
	return amount, currency, nil
}

// --- 巢狀 JSON 存取輔助 ---

// nestedSlice 依路徑逐層取值，最終值必須是 []any，否則回傳 nil。
func nestedSlice(m map[string]any, path ...string) []any {
	cur := m
	for i, key := range path {
		if i == len(path)-1 {
			s, _ := cur[key].([]any)
			return s
		}
		next, ok := cur[key].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return nil
}

// jsonFloat 安全取出數值欄位（JSON 解碼後一律為 float64）。
func jsonFloat(m map[string]any, key string) float64 {
	f, _ := m[key].(float64)
	return f
}
