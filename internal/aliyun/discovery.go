// 實例探索 API：DescribeRegions（列 region）與分頁 DescribeInstances
// （掃描實例），對應 discover_ecs_regions / discover_ecs_instances。
// 附 CompactError（對應 compact_error + _redact_request_details）供面板
// 錯誤脫敏使用。
package aliyun

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// NewDiscoveryClient 為套件級工廠變數：測試中替換為指向 httptest 假 ECS 的客戶端。
var NewDiscoveryClient = New

// DiscoverRegions 列出帳號可用 Region（固定以 ecs.aliyuncs.com 簽名），
// 對應 discover_ecs_regions。
func DiscoverRegions(ak, sk string) ([]string, error) {
	accessKey := strings.TrimSpace(ak)
	secretKey := strings.TrimSpace(sk)
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("AccessKey ID 和 AccessKey Secret 不能为空")
	}
	client := NewDiscoveryClient(accessKey, secretKey, "ecs.aliyuncs.com", "2014-05-26")
	data, err := client.Call("DescribeRegions", nil)
	if err != nil {
		return nil, err
	}
	items := nestedSlice(data, "Regions", "Region")
	if items == nil {
		return nil, fmt.Errorf("ECS 返回 Region 列表格式无法识别")
	}
	var result []string
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		region := strings.TrimSpace(strField(m, "RegionId"))
		if region != "" && !containsStr(result, region) {
			result = append(result, region)
		}
	}
	return result, nil
}

// DiscoveredInstance 為單一探索到的實例摘要（欄位對應上游 result dict）。
type DiscoveredInstance struct {
	Region       string            `json:"region"`
	InstanceID   string            `json:"instance_id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	ZoneID       string            `json:"zone_id"`
	InstanceType string            `json:"instance_type"`
	PublicIP     string            `json:"public_ip"`
	Tags         map[string]string `json:"tags"`
}

// DiscoveryResult 為掃描結果：instances / errors / regions。
type DiscoveryResult struct {
	Instances []DiscoveredInstance `json:"instances"`
	Errors    []DiscoveryError     `json:"errors"`
	Regions   []string             `json:"regions"`
}

// DiscoveryError 為單一 region 的掃描失敗摘要。
type DiscoveryError struct {
	Region string `json:"region"`
	Error  string `json:"error"`
}

// DiscoverInstances 掃描多個 Region 的 ECS 實例（每 region 分頁 PageSize=100、
// 最多 100 頁），tag_key/tag_value 於客戶端過濾；單一 region 失敗不中斷整體
// 掃描（錯誤收進 errors）。對應 discover_ecs_instances。
func DiscoverInstances(ak, sk string, regions []string, tagKey, tagValue string) (*DiscoveryResult, error) {
	accessKey := strings.TrimSpace(ak)
	secretKey := strings.TrimSpace(sk)
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("AccessKey ID 和 AccessKey Secret 不能为空")
	}
	var regionValues []string
	for _, value := range regions {
		region := strings.TrimSpace(value)
		if region != "" && !containsStr(regionValues, region) {
			regionValues = append(regionValues, region)
		}
	}
	if len(regionValues) == 0 {
		return nil, fmt.Errorf("至少需要一个 Region ID")
	}
	if len(regionValues) > 50 {
		return nil, fmt.Errorf("一次最多扫描 50 个 Region")
	}
	tagKey = strings.TrimSpace(tagKey)
	tagValue = strings.TrimSpace(tagValue)
	result := &DiscoveryResult{Regions: regionValues}
	for _, region := range regionValues {
		// 每 region 獨立 defer 捕捉：錯誤只記入 errors，不中斷其他 region
		func() {
			client := NewDiscoveryClient(accessKey, secretKey, "ecs."+region+".aliyuncs.com", "2014-05-26")
			for page := 1; page <= 100; page++ {
				data, err := client.Call("DescribeInstances", map[string]string{
					"RegionId":   region,
					"PageNumber": strconv.Itoa(page),
					"PageSize":   "100",
				})
				if err != nil {
					result.Errors = append(result.Errors, DiscoveryError{
						Region: region,
						Error:  CompactError(err, []string{accessKey, secretKey}),
					})
					return
				}
				instances := nestedSlice(data, "Instances", "Instance")
				if instances == nil {
					result.Errors = append(result.Errors, DiscoveryError{
						Region: region,
						Error:  "ECS 返回实例列表格式无法识别",
					})
					return
				}
				for _, item := range instances {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					tags := instanceTags(m)
					if tagKey != "" {
						if _, ok := tags[tagKey]; !ok {
							continue
						}
						if tagValue != "" && tags[tagKey] != tagValue {
							continue
						}
					}
					instanceID := strings.TrimSpace(strField(m, "InstanceId"))
					if instanceID == "" {
						continue
					}
					name := strField(m, "InstanceName")
					if name == "" {
						name = instanceID
					}
					status := strField(m, "Status")
					if status == "" {
						status = "Unknown"
					}
					result.Instances = append(result.Instances, DiscoveredInstance{
						Region:       region,
						InstanceID:   instanceID,
						Name:         name,
						Status:       status,
						ZoneID:       strField(m, "ZoneId"),
						InstanceType: strField(m, "InstanceType"),
						PublicIP:     firstPublicIP(m),
						Tags:         tags,
					})
				}
				total := int(jsonFloat(data, "TotalCount"))
				if total == 0 {
					total = len(instances)
				}
				if page*100 >= total || len(instances) == 0 {
					return
				}
			}
		}()
	}
	return result, nil
}

// instanceTags 解析 Tags.Tag[] 為 key→value 對照，對應 _instance_tags。
func instanceTags(instance map[string]any) map[string]string {
	tags := map[string]string{}
	for _, item := range nestedSlice(instance, "Tags", "Tag") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := strField(m, "TagKey")
		if key != "" {
			tags[key] = strField(m, "TagValue")
		}
	}
	return tags
}

// firstPublicIP 取 PublicIpAddress.IpAddress[0]（無則空字串）。
func firstPublicIP(instance map[string]any) string {
	if ips := nestedSlice(instance, "PublicIpAddress", "IpAddress"); len(ips) > 0 {
		if s, ok := ips[0].(string); ok {
			return s
		}
	}
	return ""
}

// strField 取 JSON 物件字串欄位。
func strField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// containsStr 判斷字串切片是否含指定值。
func containsStr(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// urlSchemeRe 比對錯誤訊息中的 URL（對應 _redact_request_details 第一段）。
var urlSchemeRe = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

// CompactError 壓平錯誤訊息、脫敏 secrets、隱藏已簽名請求參數、截 500 字，
// 對應 compact_error + _redact_request_details（urllib3 專屬分支為 Python
// 措辭，Go 錯誤訊息不會出現，僅保留 URL 遮蔽）。
func CompactError(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	text := strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " ")), " ")
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	text = urlSchemeRe.ReplaceAllStringFunc(text, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil {
			return "[请求地址已隐藏]"
		}
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		suffix := ""
		if parsed.RawQuery != "" {
			suffix = "?[请求参数已隐藏]"
		}
		return parsed.Scheme + "://" + parsed.Host + path + suffix
	})
	if len(text) > 500 {
		text = text[:500]
	}
	if text == "" {
		return fmt.Sprintf("%T", err)
	}
	return text
}
