// Package xtime 集中定義時間語意：
//   - CDT 月結算（月份 key、每月 1 日重置）一律使用固定 UTC+8，對應 CDT_TIMEZONE
//   - 每日排程使用 schedule.timezone 設定的時區（預設 Asia/Taipei，空值/system/local
//     為系統本地時區）；日誌、通知、狀態戳記仍使用系統本地時區
//   - state.json 中時間戳為「ISO 8601 字串 + epoch 秒」雙格式並存，兩種都必須能解析
//
// 注意：Go 的 time.Local 在首次使用時載入 /etc/localtime 後快取，主機改時區後
// 必須重啟服務才會生效（與 Python 版行為一致）。
package xtime

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScheduleDefaultZone 為每日排程的預設時區（IANA 名稱）。
// 排程不再依賴部署機器的系統時區，避免 VPS 時區錯誤導致排程平移。
const ScheduleDefaultZone = "Asia/Taipei"

// CDT 為 CDT 流量月結算專用的固定時區（UTC+8），對應 Python 版的
// dt.timezone(dt.timedelta(hours=8))。
var CDT = time.FixedZone("UTC+8", 8*3600)

// ISOFormat 對應 Python 的 datetime.isoformat(timespec="seconds")（帶本地時區偏移）。
const ISOFormat = "2006-01-02T15:04:05Z07:00"

// NowISO 回傳本地時區的 ISO 秒級時間戳。
func NowISO() string { return time.Now().Format(ISOFormat) }

// MonthKey 回傳 UTC+8 視角的 %Y-%m，對應 cdt_month_key()。
func MonthKey(now time.Time) string {
	return now.In(CDT).Format("2006-01")
}

// NextCDTReset 回傳下個月 1 日 00:00（UTC+8），對應 next_cdt_reset_at()。
// time.Date 會自動正規化月份溢出（12 月 → 次年 1 月）。
func NextCDTReset(now time.Time) time.Time {
	n := now.In(CDT)
	return time.Date(n.Year(), n.Month()+1, 1, 0, 0, 0, 0, CDT)
}

// ParseStamp 解析 state 中可能出現的時間戳：ISO 8601 字串或 epoch 秒數。
// 對應 Python 端 fromisoformat / fromtimestamp 的多來源 fallback。
func ParseStamp(v any) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	case float64:
		if t <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(t), 0), true
	case int64:
		if t <= 0 {
			return time.Time{}, false
		}
		return time.Unix(t, 0), true
	default:
		return time.Time{}, false
	}
}

// FormatDuration 將秒數轉為人類可讀字串，對應 format_duration()。
func FormatDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := minutes / 60
	if hours < 24 {
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes%60)
	}
	return fmt.Sprintf("%d 天 %d 小时", hours/24, hours%24)
}

// scheduleZoneCache 快取已解析的排程時區，避免排程熱路徑反覆載入 zoneinfo。
var scheduleZoneCache sync.Map // string -> *time.Location

// LoadScheduleLocation 解析排程時區名稱，接受三種寫法：
//   - "" / "system" / "local"（不分大小寫）→ time.Local（系統本地時區）
//   - "UTC+8"、"UTC-5:30" 等 UTC 偏移 → 固定偏移時區（不隨夏令時間變動）
//   - 其餘按 IANA 名稱解析（如 Asia/Taipei、Asia/Shanghai、Etc/GMT-8）；
//     主程式已內嵌 time/tzdata，主機缺 zoneinfo 檔也能解析
//
// 無法識別時回傳錯誤，呼叫端應比照 HH:MM 驗證失敗停用排程。
func LoadScheduleLocation(name string) (*time.Location, error) {
	text := strings.TrimSpace(name)
	key := strings.ToLower(text)
	switch key {
	case "", "system", "local":
		return time.Local, nil
	}
	if cached, ok := scheduleZoneCache.Load(key); ok {
		return cached.(*time.Location), nil
	}
	loc, err := parseScheduleLocation(text)
	if err != nil {
		return nil, err
	}
	scheduleZoneCache.Store(key, loc)
	return loc, nil
}

func parseScheduleLocation(text string) (*time.Location, error) {
	offset, ok := parseUTCOffset(text)
	if ok {
		return time.FixedZone(text, offset), nil
	}
	loc, err := time.LoadLocation(text)
	if err != nil {
		return nil, fmt.Errorf("無法識別的時區 %q（可用 IANA 名稱如 Asia/Taipei、UTC+8 偏移或 system）", text)
	}
	return loc, nil
}

// parseUTCOffset 解析 "UTC±H" / "UTC±H:MM" 寫法，回傳與 UTC 的秒差。
func parseUTCOffset(text string) (int, bool) {
	rest, found := strings.CutPrefix(strings.ToUpper(text), "UTC")
	if !found || rest == "" {
		return 0, false
	}
	sign := 1
	switch rest[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	rest = rest[1:]
	hourText, minText := rest, ""
	hasColon := false
	if h, m, found := strings.Cut(rest, ":"); found {
		hourText, minText, hasColon = h, m, true
	}
	hour, err := strconv.Atoi(hourText)
	if err != nil || hour > 23 || (len(hourText) != 1 && len(hourText) != 2) {
		return 0, false
	}
	minute := 0
	if hasColon || minText != "" {
		// 帶冒號時分鐘必須恰好兩位（"UTC+8:" 非法）
		if len(minText) != 2 {
			return 0, false
		}
		minute, err = strconv.Atoi(minText)
		if err != nil || minute > 59 {
			return 0, false
		}
	}
	return sign * (hour*3600 + minute*60), true
}
