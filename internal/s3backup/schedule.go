// 排程 slot 計算，對應 schedule_slot：hourly/daily/weekly 三種週期，
// slot 字串格式為跨版本契約（與上游 s3-backup-state.json 完全一致）。
package s3backup

import "time"

// ScheduleSlot 回傳當前時刻對應的 slot 字串；未到排程時間回傳空字串。
func ScheduleSlot(cfg *Config, now time.Time) string {
	hour, minute := parseHHMM(cfg.Time)
	switch cfg.Schedule {
	case "hourly":
		target := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), minute, 0, 0, now.Location())
		if now.Before(target) {
			return ""
		}
		return now.Format("hourly:2006010215")
	case "daily":
		target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if now.Before(target) {
			return ""
		}
		return now.Format("daily:20060102")
	case "weekly":
		// 週一起算的週首（Python weekday(): 週一=0）
		pyWeekday := (int(now.Weekday()) + 6) % 7
		weekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
			AddDate(0, 0, -pyWeekday)
		target := weekStart.AddDate(0, 0, cfg.Weekday).
			Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
		if now.Before(target) {
			return ""
		}
		return "weekly:" + weekStart.Format("20060102")
	}
	return ""
}

// parseHHMM 解析已驗證的 HH:MM。
func parseHHMM(text string) (int, int) {
	if len(text) != 5 {
		return 0, 0
	}
	hour := int(text[0]-'0')*10 + int(text[1]-'0')
	minute := int(text[3]-'0')*10 + int(text[4]-'0')
	return hour, minute
}
