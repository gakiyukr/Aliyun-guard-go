// xtime 的時間語意測試：CDT 月結算固定 UTC+8、跨月邊界、雙格式時間戳解析。
package xtime

import (
	"testing"
	"time"
)

func TestMonthKeyUsesFixedUTC8(t *testing.T) {
	// UTC 2026-01-31 20:00 = 台北 2026-02-01 04:00 → 月份 key 必須是 2026-02
	got := MonthKey(time.Date(2026, 1, 31, 20, 0, 0, 0, time.UTC))
	if got != "2026-02" {
		t.Fatalf("MonthKey = %s, want 2026-02", got)
	}
}

func TestNextCDTReset(t *testing.T) {
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{ // 一般月份 → 次月 1 日
			time.Date(2026, 9, 10, 8, 30, 0, 0, CDT),
			time.Date(2026, 10, 1, 0, 0, 0, 0, CDT),
		},
		{ // 12 月 → 次年 1 月
			time.Date(2026, 12, 15, 23, 0, 0, 0, CDT),
			time.Date(2027, 1, 1, 0, 0, 0, 0, CDT),
		},
	}
	for _, c := range cases {
		if got := NextCDTReset(c.now); !got.Equal(c.want) {
			t.Fatalf("NextCDTReset(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}

func TestParseStamp(t *testing.T) {
	// ISO 帶偏移
	parsed, ok := ParseStamp("2026-09-10T08:30:00+08:00")
	if !ok || parsed.Unix() != time.Date(2026, 9, 10, 0, 30, 0, 0, time.UTC).Unix() {
		t.Fatalf("ISO 解析錯誤: %v %v", parsed, ok)
	}
	// epoch 秒
	parsed, ok = ParseStamp(float64(1789027200))
	if !ok || parsed.Unix() != 1789027200 {
		t.Fatalf("epoch 解析錯誤: %v %v", parsed, ok)
	}
	// 無效值
	if _, ok := ParseStamp(""); ok {
		t.Fatal("空字串不應解析成功")
	}
	if _, ok := ParseStamp("not-a-time"); ok {
		t.Fatal("非法字串不應解析成功")
	}
}

func TestLoadScheduleLocation(t *testing.T) {
	// IANA 名稱（主程式內嵌 tzdata，不依賴主機 zoneinfo）
	taipei, err := LoadScheduleLocation("Asia/Taipei")
	if err != nil {
		t.Fatalf("Asia/Taipei 應可解析: %v", err)
	}
	if _, off := time.Date(2026, 9, 10, 0, 0, 0, 0, taipei).Zone(); off != 8*3600 {
		t.Fatalf("Asia/Taipei 應為 UTC+8，got %d", off)
	}
	// UTC 偏移寫法
	utc8, err := LoadScheduleLocation("UTC+8")
	if err != nil || utc8.String() != "UTC+8" {
		t.Fatalf("UTC+8 應解析為固定時區: %v %v", utc8, err)
	}
	if _, off := time.Date(2026, 1, 1, 0, 0, 0, 0, utc8).Zone(); off != 8*3600 {
		t.Fatalf("UTC+8 偏移應為 8 小時，got %d", off)
	}
	neg, err := LoadScheduleLocation("UTC-5:30")
	if err != nil {
		t.Fatalf("UTC-5:30 應可解析: %v", err)
	}
	if _, off := time.Date(2026, 1, 1, 0, 0, 0, 0, neg).Zone(); off != -5*3600-1800 {
		t.Fatalf("UTC-5:30 偏移應為 -5.5 小時，got %d", off)
	}
	// 空值 / system / local → 系統本地時區（不分大小寫、容忍空白）
	for _, name := range []string{"", "system", "Local", " local "} {
		loc, err := LoadScheduleLocation(name)
		if err != nil || loc != time.Local {
			t.Fatalf("%q 應回傳系統本地時區: %v %v", name, loc, err)
		}
	}
	// 非法值（"UTC" 本身為合法 IANA 名稱，不在拒絕之列）
	for _, name := range []string{"No/SuchZone", "UTC+25", "UTC+8:5", "UTC+8:", "UTC±8"} {
		if _, err := LoadScheduleLocation(name); err == nil {
			t.Fatalf("%q 應解析失敗", name)
		}
	}
	// 快取：同名稱重複呼叫應回傳同一指標
	again, _ := LoadScheduleLocation("Asia/Taipei")
	if again != taipei {
		t.Fatal("快取應回傳同一時區實例")
	}
}
