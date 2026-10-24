// Package tgbot 對等移植 telegram_control.py：管理員專用的 Telegram 控制
// Bot（getUpdates 長輪詢）。單訊息面板 + inline 鍵盤導航 + 危險操作二次確認。
//
// 安全模型（與上游一致）：
//   - 僅私聊處理；非私聊一律忽略（提示「仅支持私聊」）。Go 擴充：配置
//     telegram.allow_group_control=true 時放行群組（供在群組中使用，見
//     docs/PLAN.md 已知擴充）。
//   - 僅 control_admin_ids（缺省回退 telegram.chat_id）內的用戶可操作。
//   - 檢測與關機需一次確認；「關機狀態開機」需連續確認兩次，第二次在
//     cycle 鎖內複查實例狀態與 CDT 流量，超閾值強制開機會自動暫停監控。
//
// 狀態持久化：telegram-control-state.json 記錄 token 指紋與 getUpdates
// offset；Token 變更或控制停用後重新啟用時丟棄啟用前的待處理訊息。
package tgbot

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/guard"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/state"
)

const (
	pollTimeoutSeconds      = 20
	retryWaitSeconds        = 5
	confirmTTLSeconds       = 90
	scheduleInputTTLSeconds = 300
)

// botCommands 對應 BOT_COMMANDS，於 Token 變更時向 Telegram 註冊命令選單。
var botCommands = []map[string]string{
	{"command": "status", "description": "查看最近检测状态"},
	{"command": "instances", "description": "查看监控实例"},
	{"command": "check", "description": "立即执行一轮检测"},
	{"command": "poweron", "description": "选择实例开机"},
	{"command": "poweroff", "description": "选择实例关机"},
	{"command": "schedule", "description": "管理定时开关机"},
	{"command": "help", "description": "显示控制菜单"},
}

type pendingItem struct {
	UserID            int64
	ChatID            int64
	Action            string // check / start / stop
	InstanceID        string
	Stage             int
	ThresholdOverride bool
	TrafficGB         float64
	LimitGB           float64
	MessageID         int64
	Expires           time.Time
}

type scheduleInput struct {
	ChatID     int64
	MessageID  int64
	InstanceID string
	Expires    time.Time
}

// service 為控制服務的運行時狀態（對應 TelegramControlService）。
type service struct {
	paths               config.Paths
	pending             map[string]pendingItem
	scheduleInputs      map[int64]scheduleInput
	offset              *int64
	fingerprint         string
	commandsFingerprint string
	drainPending        bool
	lastError           string
	lastInactive        string
	botUsername         string
	// 鉤子（測試用；nil 時用真實實作）
	runCycleFn    func(paths config.Paths, noNotify bool) string
	controlFn     func(paths config.Paths, index int, action string) (string, error)
	queryStatusFn func(paths config.Paths, index int) (string, float64, float64, error)
}

// Run 進入控制迴圈，直到進程結束（守護模式由 cmd 層負責信號）。
func Run(paths config.Paths) error {
	s := &service{
		paths:          paths,
		pending:        map[string]pendingItem{},
		scheduleInputs: map[int64]scheduleInput{},
		drainPending:   true,
	}
	for {
		cfg, tg, admins, ok := s.pollConfig()
		if !ok {
			time.Sleep(retryWaitSeconds)
			continue
		}
		if err := s.handleLoop(cfg, tg, admins); err != nil {
			if err.Error() != s.lastError {
				log.Printf("Telegram Bot 控制轮询失败: %v", err)
				s.lastError = err.Error()
			}
			time.Sleep(retryWaitSeconds)
			continue
		}
	}
}

// pollConfig 每輪重讀配置；控制未啟用/缺 Token/缺管理員時回傳 false。
func (s *service) pollConfig() (map[string]any, map[string]any, map[int64]bool, bool) {
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		log.Printf("Telegram Bot 控制未运行: 配置读取失败: %v", err)
		return nil, nil, nil, false
	}
	tg := config.GetMap(cfg, "telegram")
	if !config.GetBool(tg, "control_enabled", true) {
		s.drainPending = true
		logIfChanged(&s.lastInactive, "Telegram Bot 控制未运行: 已在配置中关闭")
		return nil, nil, nil, false
	}
	if strings.TrimSpace(config.GetStr(tg, "bot_token")) == "" {
		s.drainPending = true
		logIfChanged(&s.lastInactive, "Telegram Bot 控制未运行: Bot Token 未配置")
		return nil, nil, nil, false
	}
	admins := controlAdminIDs(tg)
	if len(admins) == 0 {
		s.drainPending = true
		logIfChanged(&s.lastInactive, "Telegram Bot 控制未运行: 未配置有效的管理员用户 ID")
		return nil, nil, nil, false
	}
	s.lastInactive = ""
	return cfg, tg, admins, true
}

// controlAdminIDs 對應 telegram_control_admin_ids()：control_admin_ids 優先，
// 否則回退正數的 telegram.chat_id。
func controlAdminIDs(tg map[string]any) map[int64]bool {
	admins := map[int64]bool{}
	for _, item := range config.GetSlice(tg, "control_admin_ids") {
		if f, ok := item.(float64); ok && f > 0 {
			admins[int64(f)] = true
		}
	}
	if len(admins) > 0 {
		return admins
	}
	if chatID, ok := toFloat(tg["chat_id"]); ok && chatID > 0 {
		admins[int64(chatID)] = true
	}
	return admins
}

// handleLoop 為單次長輪詢迭代；連線類錯誤回傳 error 由 Run 退避重試。
func (s *service) handleLoop(cfg, tg map[string]any, admins map[int64]bool) error {
	if err := s.prepareToken(tg); err != nil {
		return err
	}
	updates, err := s.getUpdates(tg)
	if err != nil {
		return err
	}
	if len(updates) > 0 {
		maxID := int64(-1)
		for _, u := range updates {
			if id, ok := toFloat(asMap(u)["update_id"]); ok && int64(id) > maxID {
				maxID = int64(id)
			}
		}
		offset := maxID + 1
		s.offset = &offset
		s.saveOffset()
		// 處理前重讀配置（與上游一致，保證熱載入即時生效）
		latestCfg, latestTg, latestAdmins, ok := s.pollConfig()
		if !ok {
			return nil
		}
		if tokenFingerprint(config.GetStr(latestTg, "bot_token")) != s.fingerprint {
			s.drainPending = true
			return nil
		}
		for _, u := range updates {
			s.handleUpdate(latestCfg, latestTg, latestAdmins, asMap(u))
		}
	}
	if s.lastError != "" {
		log.Printf("Telegram Bot 控制连接已恢复")
		s.lastError = ""
	}
	return nil
}

// --- 面板文字組裝（對應 build_status_text / build_instances_text / build_schedule_text）---

func buildStatusText(paths config.Paths) string {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return "配置读取失败"
	}
	st, err := state.Load(paths.State)
	if err != nil {
		st = map[string]any{}
	}
	finished, _ := st["last_cycle_finished_at"].(string)
	result := "暂无"
	if finished != "" {
		result = "存在错误"
		if ok, _ := st["last_cycle_ok"].(bool); ok {
			result = "正常"
		}
	}
	if finished == "" {
		finished = "尚未完成检测"
	}
	lines := []string{
		"Aliyun Guard 状态",
		fmt.Sprintf("最近检测: %s", finished),
		fmt.Sprintf("检测结果: %s", result),
		fmt.Sprintf("检测次数: %d", int(toFloatOr(st["cycle_count"]))),
		fmt.Sprintf("监控实例: %d 个", len(config.Users(cfg))),
	}
	if te, _ := st["telegram_error"].(string); strings.TrimSpace(te) != "" {
		lines = append(lines, "通知状态: 上次发送失败")
	}
	return strings.Join(lines, "\n")
}

func buildInstancesText(paths config.Paths) string {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return "配置读取失败"
	}
	st, _ := state.Load(paths.State)
	previous, _ := st["instances"].(map[string]any)
	users := config.Users(cfg)
	if len(users) == 0 {
		return "尚未配置监控实例。"
	}
	var b strings.Builder
	b.WriteString("监控实例")
	for i, user := range users {
		instanceID := config.GetStr(user, "instance_id")
		cur, _ := previous[instanceID].(map[string]any)
		status := "Unknown"
		if cur != nil {
			if v, _ := cur["status_after"].(string); v != "" {
				status = v
			}
		}
		mode := "监控中"
		if config.GetBool(user, "paused", false) {
			mode = "已暂停"
		}
		fmt.Fprintf(&b, "\n\n#%d %s\nID: %s\n状态: %s · %s\n流量: %s / %.2f GB",
			i+1, orDefault(config.GetStr(user, "name"), instanceID), instanceID,
			status, mode, formatTraffic(cur["traffic_gb"]),
			config.GetFloat(user, "traffic_limit_gb", 0))
	}
	return b.String()
}

// buildScheduleText 對應 build_schedule_text()。排程時刻以 schedule.timezone
// 指定的時區呈現（預設 Asia/Taipei），並附伺服器本地時間供對照。
func buildScheduleText(paths config.Paths, user map[string]any, now time.Time) string {
	schedule := guard.GetScheduleConfig(user)
	name := orDefault(config.GetStr(user, "name"), config.GetStr(user, "instance_id"))
	loc := guard.ScheduleLocation(schedule)
	lines := []string{
		"定时开关机",
		fmt.Sprintf("实例: %s (%s)", name, config.GetStr(user, "instance_id")),
		fmt.Sprintf("服务器时间: %s", now.Format("2006-01-02 15:04 -0700")),
		fmt.Sprintf("计划状态: %s", map[bool]string{true: "已启用", false: "已关闭"}[schedule["enabled"] == true]),
		fmt.Sprintf("排程时区: %s（当前 %s）", schedule["timezone"], now.In(loc).Format("2006-01-02 15:04")),
		fmt.Sprintf("每日开机: %v", schedule["start_time"]),
		fmt.Sprintf("每日关机: %v", schedule["stop_time"]),
	}
	if schedule["enabled"] == true {
		target := guard.ScheduleTarget(user, now)
		eventAt, eventAction, ok := guard.NextScheduleEvent(user, now)
		startTime, _ := schedule["start_time"].(string)
		stopTime, _ := schedule["stop_time"].(string)
		lines = append(lines, fmt.Sprintf("运行时段: %s",
			map[bool]string{true: "跨午夜", false: "当日"}[startTime > stopTime]))
		lines = append(lines, fmt.Sprintf("当前目标: %s",
			map[bool]string{true: "运行", false: "关机"}[fmt.Sprintf("%v", target) == "running"]))
		if ok {
			lines = append(lines, fmt.Sprintf("下一动作: %s %s（排程时区）",
				eventAt.Format("2006-01-02 15:04"),
				map[string]string{"start": "开机", "stop": "关机"}[eventAction]))
		}
	}
	if config.GetBool(user, "paused", false) {
		lines = append(lines, "提示: 实例监控已暂停，计划暂不执行。")
	} else {
		lines = append(lines, "提示: 达到流量阈值时不会执行计划开机。")
	}
	return strings.Join(lines, "\n")
}

// resolveInstance 對應 resolve_instance()：序號優先，其後按 ID/名稱唯一匹配。
func resolveInstance(users []map[string]any, selector string) (int, map[string]any) {
	selector = strings.TrimSpace(selector)
	if isAllDigits(selector) {
		idx := 0
		for _, r := range selector {
			idx = idx*10 + int(r-'0')
		}
		if idx >= 1 && idx <= len(users) {
			return idx - 1, users[idx-1]
		}
	}
	if selector == "" {
		return -1, nil
	}
	lower := strings.ToLower(selector)
	match := -1
	for i, user := range users {
		id := strings.ToLower(config.GetStr(user, "instance_id"))
		name := strings.ToLower(config.GetStr(user, "name"))
		if lower == id || lower == name {
			if match >= 0 {
				return -1, nil // 多個匹配視為無效
			}
			match = i
		}
	}
	if match < 0 {
		return -1, nil
	}
	return match, users[match]
}

// --- Telegram API 薄封裝 ---

func (s *service) tgAPI(tg map[string]any, method string, payload map[string]any, longPoll bool) (map[string]any, error) {
	token := config.GetStr(tg, "bot_token")
	base := config.GetStr(tg, "api_base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	timeout := config.GetInt(tg, "timeout_seconds", 12)
	if longPoll {
		timeout = pollTimeoutSeconds + 10
	}
	client := &http.Client{Timeout: time.Duration(timeout+10) * time.Second}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// result 可能是物件、布林（setMyCommands/answerCallbackQuery/deleteMessage 等）
	// 或陣列（getUpdates 由 tgAPIRaw 處理）：先以 RawMessage 接收再按型別解析。
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	// 回應偶發被截斷（HTTP 200 但 body 非 JSON）：最多重試一次
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		resp, err := client.Post(base+"/bot"+token+"/"+method, "application/json", strings.NewReader(string(raw)))
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err := json.Unmarshal(body, &envelope); err == nil {
			if !envelope.OK {
				return nil, fmt.Errorf("Telegram API %s: %s", method, envelope.Description)
			}
			out := map[string]any{}
			if len(envelope.Result) > 0 {
				_ = json.Unmarshal(envelope.Result, &out) // 非物件結果回空 map
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("Telegram API %s: 回應非 JSON（重試後仍失敗）", method)
}

// tgAPIRaw 回傳 result 為陣列的回應（getUpdates）。
func (s *service) tgAPIRaw(tg map[string]any, method string, payload map[string]any) ([]any, error) {
	token := config.GetStr(tg, "bot_token")
	base := config.GetStr(tg, "api_base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := &http.Client{Timeout: (pollTimeoutSeconds + 20) * time.Second}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(base+"/bot"+token+"/"+method, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []any  `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("HTTP %d: 回應非 JSON", resp.StatusCode)
	}
	if !result.OK {
		return nil, fmt.Errorf("Telegram API %s: %s", method, result.Description)
	}
	return result.Result, nil
}

func (s *service) getUpdates(tg map[string]any) ([]any, error) {
	offset := int64(0)
	if s.offset != nil {
		offset = *s.offset
	}
	return s.tgAPIRaw(tg, "getUpdates", map[string]any{
		"offset":          offset,
		"limit":           100,
		"timeout":         pollTimeoutSeconds,
		"allowed_updates": []string{"message", "callback_query"},
	})
}

// --- 發送/編輯/回調輔助（對應 _send/_edit/_display/_answer_callback/_close_menu）---

func markupJSON(markup map[string]any) string {
	raw, _ := json.Marshal(markup)
	return string(raw)
}

func (s *service) send(cfg map[string]any, chatID int64, text string, markup map[string]any) {
	tg := config.GetMap(cfg, "telegram")
	chunks := splitMessage(text)
	for i, chunk := range chunks {
		payload := map[string]any{"chat_id": fmt.Sprintf("%d", chatID), "text": chunk}
		if markup != nil && i == len(chunks)-1 {
			payload["reply_markup"] = markupJSON(markup)
		}
		if _, err := s.tgAPI(tg, "sendMessage", payload, false); err != nil {
			log.Printf("[Bot] sendMessage 失败: %v", err)
		}
	}
}

func callbackMessageRef(callback map[string]any) (int64, int64) {
	chatID, ok1 := toFloat(nested(callback, "message", "chat")["id"])
	messageID, ok2 := toFloat(nested(callback, "message")["message_id"])
	if !ok1 || !ok2 {
		return 0, 0
	}
	return int64(chatID), int64(messageID)
}

func (s *service) edit(cfg map[string]any, chatID, messageID int64, text string, markup map[string]any) {
	tg := config.GetMap(cfg, "telegram")
	chunks := splitMessage(text)
	if len(chunks) != 1 {
		s.send(cfg, chatID, text, markup)
		return
	}
	if markup == nil {
		markup = map[string]any{"inline_keyboard": []any{}}
	}
	payload := map[string]any{
		"chat_id":      fmt.Sprintf("%d", chatID),
		"message_id":   fmt.Sprintf("%d", messageID),
		"text":         chunks[0],
		"reply_markup": markupJSON(markup),
	}
	if _, err := s.tgAPI(tg, "editMessageText", payload, false); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return
		}
		log.Printf("Telegram 消息编辑失败，改为发送新消息: %v", err)
		s.send(cfg, chatID, text, markup)
	}
}

func (s *service) display(cfg map[string]any, chatID int64, text string, markup map[string]any, messageID int64) {
	if messageID != 0 {
		s.edit(cfg, chatID, messageID, text, markup)
		return
	}
	s.send(cfg, chatID, text, markup)
}

func (s *service) editCallback(cfg map[string]any, callback map[string]any, text string, markup map[string]any) {
	chatID, messageID := callbackMessageRef(callback)
	if chatID == 0 {
		return
	}
	s.edit(cfg, chatID, messageID, text, markup)
}

func (s *service) answerCallback(tg map[string]any, callbackID string, text string, alert bool) {
	payload := map[string]any{
		"callback_query_id": callbackID,
		"text":              truncate(text, 190),
		"show_alert":        map[bool]string{true: "true", false: "false"}[alert],
	}
	if _, err := s.tgAPI(tg, "answerCallbackQuery", payload, false); err != nil {
		log.Printf("Telegram 回调确认失败: %v", err)
	}
}

func (s *service) closeMenu(tg map[string]any, callback map[string]any) {
	chatID, messageID := callbackMessageRef(callback)
	if chatID == 0 {
		return
	}
	payload := map[string]any{"chat_id": fmt.Sprintf("%d", chatID), "message_id": fmt.Sprintf("%d", messageID)}
	if _, err := s.tgAPI(tg, "deleteMessage", payload, false); err != nil {
		log.Printf("Telegram 菜单删除失败，改为收起按钮: %v", err)
		s.edit(tgToCfg(tg), chatID, messageID,
			"Aliyun Guard Bot 菜单已关闭。\n\n发送 /help 可重新打开菜单。", nil)
	}
}

// --- 鍵盤佈局與主文字（逐字對應 _menu_markup/_main_text/_view_markup）---

func menuMarkup() map[string]any {
	return map[string]any{"inline_keyboard": []any{
		[]any{
			map[string]string{"text": "📊 状态", "callback_data": "ag:status"},
			map[string]string{"text": "🖥 实例", "callback_data": "ag:instances"},
		},
		[]any{map[string]string{"text": "🔍 立即检测", "callback_data": "ag:req:check"}},
		[]any{
			map[string]string{"text": "▶ 实例开机", "callback_data": "ag:list:start"},
			map[string]string{"text": "⏹ 实例关机", "callback_data": "ag:list:stop"},
		},
		[]any{map[string]string{"text": "🕒 定时计划", "callback_data": "ag:schedule"}},
		[]any{map[string]string{"text": "✖ 关闭菜单", "callback_data": "ag:close"}},
	}}
}

func mainText() string {
	return "Aliyun Guard Bot 控制\n\n" +
		"/status - 查看最近检测状态\n" +
		"/instances - 查看监控实例\n" +
		"/check - 立即执行一轮检测\n" +
		"/poweron <序号或实例ID> - 开机\n" +
		"/poweroff <序号或实例ID> - 关机\n" +
		"/schedule [序号或实例ID] - 定时计划\n" +
		"/help - 显示控制菜单\n\n" +
		"检测和关机需要确认；关机状态开机需要连续确认两次。"
}

func viewMarkup(refreshData string) map[string]any {
	return map[string]any{"inline_keyboard": []any{
		[]any{
			map[string]string{"text": "刷新", "callback_data": refreshData},
			map[string]string{"text": "返回主菜单", "callback_data": "ag:menu"},
		},
	}}
}

func (s *service) sendHelp(cfg map[string]any, chatID int64, messageID int64) {
	s.display(cfg, chatID, mainText(), menuMarkup(), messageID)
}

// --- 面板視圖（狀態/實例/開關機選單/排程）---

func (s *service) showStatus(cfg map[string]any, chatID int64, messageID int64) {
	s.display(cfg, chatID, buildStatusText(s.paths), viewMarkup("ag:status"), messageID)
}

func (s *service) showInstances(cfg map[string]any, chatID int64, messageID int64) {
	s.display(cfg, chatID, buildInstancesText(s.paths), viewMarkup("ag:instances"), messageID)
}

func (s *service) instanceChoices(cfg map[string]any, chatID int64, action string, messageID int64) {
	label := "关机"
	if action == "start" {
		label = "开机"
	}
	users := config.Users(mustLoadConfig(s.paths))
	var rows []any
	for i, user := range users {
		name := truncate(orDefault(config.GetStr(user, "name"), config.GetStr(user, "instance_id")), 32)
		rows = append(rows, []any{map[string]string{
			"text":          label + " " + name,
			"callback_data": fmt.Sprintf("ag:req:%s:%d", action, i),
		}})
	}
	if len(rows) == 0 {
		s.display(cfg, chatID, "尚未配置监控实例。", viewMarkup("ag:list:"+action), messageID)
		return
	}
	rows = append(rows, []any{map[string]string{"text": "返回主菜单", "callback_data": "ag:menu"}})
	s.display(cfg, chatID, "请选择需要"+label+"的实例：", map[string]any{"inline_keyboard": rows}, messageID)
}

func (s *service) scheduleChoices(cfg map[string]any, chatID int64, messageID int64) {
	cfgData := mustLoadConfig(s.paths)
	users := config.Users(cfgData)
	var rows []any
	for i, user := range users {
		schedule := guard.GetScheduleConfig(user)
		name := truncate(orDefault(config.GetStr(user, "name"), config.GetStr(user, "instance_id")), 24)
		suffix := "已关闭"
		if schedule["enabled"] == true {
			suffix = fmt.Sprintf("%v-%v", schedule["start_time"], schedule["stop_time"])
		}
		rows = append(rows, []any{map[string]string{
			"text":          name + " · " + suffix,
			"callback_data": fmt.Sprintf("ag:sched:view:%d", i),
		}})
	}
	rows = append(rows, []any{map[string]string{"text": "返回主菜单", "callback_data": "ag:menu"}})
	text := "请选择需要管理定时计划的实例："
	if len(users) == 0 {
		text = "尚未配置监控实例。"
	}
	s.display(cfg, chatID, text, map[string]any{"inline_keyboard": rows}, messageID)
}

func (s *service) scheduleDetail(cfg map[string]any, chatID int64, index int, messageID int64) {
	users := config.Users(mustLoadConfig(s.paths))
	if index < 0 || index >= len(users) {
		s.display(cfg, chatID, "实例不存在或配置已经变化。", viewMarkup("ag:schedule"), messageID)
		return
	}
	user := users[index]
	schedule := guard.GetScheduleConfig(user)
	toggleLabel := "关闭计划"
	if schedule["enabled"] != true {
		toggleLabel = "启用计划"
	}
	markup := map[string]any{"inline_keyboard": []any{
		[]any{
			map[string]string{"text": "修改时间", "callback_data": fmt.Sprintf("ag:sched:edit:%d", index)},
			map[string]string{"text": toggleLabel, "callback_data": fmt.Sprintf("ag:sched:ask:%d", index)},
		},
		[]any{
			map[string]string{"text": "刷新", "callback_data": fmt.Sprintf("ag:sched:view:%d", index)},
			map[string]string{"text": "返回实例列表", "callback_data": "ag:schedule"},
		},
		[]any{map[string]string{"text": "返回主菜单", "callback_data": "ag:menu"}},
	}}
	s.display(cfg, chatID, buildScheduleText(s.paths, user, time.Now()), markup, messageID)
}

// --- 二次確認（對應 _new_confirmation/_expire_pending/_execute_pending）---

func newToken() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// newConfirmation 清除同用戶舊待確認項後建立新確認（check/start/stop）。
func (s *service) newConfirmation(
	cfg map[string]any, chatID, userID int64, action, instanceID string,
	stage int, thresholdOverride bool, trafficGB, limitGB float64, messageID int64,
) {
	s.expirePending()
	for token, item := range s.pending {
		if item.UserID == userID {
			delete(s.pending, token)
		}
	}
	token := newToken()
	s.pending[token] = pendingItem{
		UserID: userID, ChatID: chatID, Action: action, InstanceID: instanceID,
		Stage: stage, ThresholdOverride: thresholdOverride,
		TrafficGB: trafficGB, LimitGB: limitGB, MessageID: messageID,
		Expires: time.Now().Add(confirmTTLSeconds * time.Second),
	}
	var text string
	if action == "check" {
		text = "确认立即执行一轮真实检测？检测可能根据当前规则执行开关机。"
	} else {
		users := config.Users(mustLoadConfig(s.paths))
		var user map[string]any
		for _, item := range users {
			if config.GetStr(item, "instance_id") == instanceID {
				user = item
				break
			}
		}
		if user == nil {
			delete(s.pending, token)
			s.display(cfg, chatID, "实例不存在或配置已经变化。", menuMarkup(), messageID)
			return
		}
		label := "关机"
		if action == "start" {
			label = "开机"
		}
		name := orDefault(config.GetStr(user, "name"), instanceID)
		switch {
		case action == "start" && stage == 1:
			text = fmt.Sprintf("第一次确认：准备%s实例 %s（%s）？", label, name, instanceID)
		case action == "start" && thresholdOverride:
			text = fmt.Sprintf("第二次确认：当前 CDT 流量 %.2f GB 已达到 %.2f GB 阈值。\n继续将强制开机，并自动暂停该实例监控。", trafficGB, limitGB)
		case action == "start":
			text = "第二次确认：实例当前已关机，确认执行开机？"
		default:
			text = fmt.Sprintf("确认%s实例 %s（%s）？", label, name, instanceID)
		}
	}
	markup := map[string]any{"inline_keyboard": []any{
		[]any{
			map[string]string{"text": "确认执行", "callback_data": "ag:confirm:" + token},
			map[string]string{"text": "取消", "callback_data": "ag:cancel:" + token},
		},
	}}
	s.display(cfg, chatID, text, markup, messageID)
}

func (s *service) expirePending() {
	now := time.Now()
	for token, item := range s.pending {
		if !item.Expires.After(now) {
			delete(s.pending, token)
		}
	}
}

// executePending 執行已確認的操作（check → run_cycle；start/stop → control_instance）。
func (s *service) executePending(cfg map[string]any, pending pendingItem) {
	chatID := pending.ChatID
	messageID := pending.MessageID

	if pending.Action == "check" {
		s.display(cfg, chatID, "正在执行检测，请稍候。", nil, messageID)
		summary := s.runCycle(cfg)
		s.display(cfg, chatID, summary, menuMarkup(), messageID)
		log.Printf("Telegram 管理员 %d 执行了一轮检测", pending.UserID)
		return
	}

	if pending.Action == "start" && pending.Stage == 1 {
		s.prepareSecondStartConfirmation(cfg, pending)
		return
	}

	index := s.indexOfInstance(pending.InstanceID)
	if index < 0 {
		s.display(cfg, chatID, "实例不存在或配置已经变化。", menuMarkup(), messageID)
		return
	}
	verb := "停止"
	if pending.Action == "start" {
		verb = "启动"
	}
	log.Printf("Telegram 管理员 %d 请求%s实例 %s", pending.UserID, verb, pending.InstanceID)
	s.display(cfg, chatID, fmt.Sprintf("正在%s实例 %s，请稍候。", verb, pending.InstanceID), nil, messageID)
	message, err := s.controlInstance(pending.Action, index, pending.ThresholdOverride)
	if err != nil {
		message = "操作失败: " + err.Error()
		s.display(cfg, chatID, message, menuMarkup(), messageID)
		return
	}
	s.display(cfg, chatID, message, menuMarkup(), messageID)
}

// prepareSecondStartConfirmation 對應 _prepare_second_start_confirmation()：
// 在 cycle 鎖內複查狀態與流量；Running 不動作、非 Stopped 暫緩、
// 達到流量閾值時進入「強制開機 + 自動暫停監控」的第二次確認。
func (s *service) prepareSecondStartConfirmation(cfg map[string]any, pending pendingItem) {
	chatID := pending.ChatID
	messageID := pending.MessageID
	status, traffic, limit, err := s.queryStatusAndTraffic(pending.InstanceID)
	if err != nil {
		if err.Error() == "已有检测任务正在运行，请稍后重新操作。" {
			s.display(cfg, chatID, err.Error(), menuMarkup(), messageID)
			return
		}
		if err.Error() == "实例不存在或配置已经变化。" {
			s.display(cfg, chatID, err.Error(), menuMarkup(), messageID)
			return
		}
		s.display(cfg, chatID, "操作失败: "+err.Error(), menuMarkup(), messageID)
		return
	}
	switch status {
	case "Running":
		s.display(cfg, chatID, "实例已经处于运行状态，无需开机。", menuMarkup(), messageID)
		return
	case "Stopped":
		// 繼續二次確認
	default:
		s.display(cfg, chatID, fmt.Sprintf("实例当前状态为 %s，暂不执行开机。", status), menuMarkup(), messageID)
		return
	}
	s.newConfirmation(cfg, chatID, pending.UserID, "start", pending.InstanceID,
		2, traffic >= limit, traffic, limit, messageID)
}

// --- 排程修改（對應 _prompt_schedule_input/_handle_schedule_input/_confirm_schedule_toggle/_set_schedule_enabled）---

func (s *service) promptScheduleInput(cfg map[string]any, chatID, userID int64, index int, messageID int64) {
	users := config.Users(mustLoadConfig(s.paths))
	if index < 0 || index >= len(users) {
		s.display(cfg, chatID, "实例不存在或配置已经变化。", viewMarkup("ag:schedule"), messageID)
		return
	}
	user := users[index]
	schedule := guard.GetScheduleConfig(user)
	s.scheduleInputs[userID] = scheduleInput{
		ChatID:     chatID,
		MessageID:  messageID,
		InstanceID: config.GetStr(user, "instance_id"),
		Expires:    time.Now().Add(scheduleInputTTLSeconds * time.Second),
	}
	text := fmt.Sprintf(
		"修改定时计划\n\n实例: %s (%s)\n当前时间: %v 开机，%v 关机\n\n"+
			"请发送新的开机和关机时间：\n格式: HH:MM HH:MM\n示例: 08:30 23:15\n\n输入有效期 5 分钟。",
		orDefault(config.GetStr(user, "name"), config.GetStr(user, "instance_id")),
		config.GetStr(user, "instance_id"),
		schedule["start_time"], schedule["stop_time"])
	s.display(cfg, chatID, text, map[string]any{"inline_keyboard": []any{
		[]any{map[string]string{"text": "取消修改", "callback_data": fmt.Sprintf("ag:sched:view:%d", index)}},
	}}, messageID)
}

// handleScheduleInput 處理排程時間輸入；回傳 false 表示非排程輸入（交回指令處理）。
func (s *service) handleScheduleInput(cfg map[string]any, userID, chatID int64, text string) bool {
	pending, ok := s.scheduleInputs[userID]
	if !ok || pending.ChatID != chatID {
		return false
	}
	messageID := pending.MessageID
	if !pending.Expires.After(time.Now()) {
		delete(s.scheduleInputs, userID)
		s.display(cfg, chatID, "时间输入已过期，请重新进入定时计划修改。", viewMarkup("ag:schedule"), messageID)
		return true
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(text, "/") && lower != "/cancel" {
		delete(s.scheduleInputs, userID)
		return false
	}
	if lower == "/cancel" {
		delete(s.scheduleInputs, userID)
		index := s.indexOfInstance(pending.InstanceID)
		s.scheduleDetail(cfg, chatID, index, messageID)
		return true
	}

	parts := strings.Fields(strings.ReplaceAll(text, ",", " "))
	var err error
	if len(parts) != 2 {
		err = fmt.Errorf("请同时输入开机时间和关机时间")
	} else {
		startTime, errStart := guard.NormalizeScheduleTime(parts[0], "开机时间")
		if errStart != nil {
			err = errStart
		} else {
			stopTime, errStop := guard.NormalizeScheduleTime(parts[1], "关机时间")
			if errStop != nil {
				err = errStop
			} else if startTime == stopTime {
				err = fmt.Errorf("开机时间和关机时间不能相同")
			} else {
				err = s.saveScheduleTimes(pending.InstanceID, startTime, stopTime)
				if err == nil {
					delete(s.scheduleInputs, userID)
					log.Printf("Telegram 管理员 %d 修改实例 %s 定时计划为 %s-%s",
						userID, pending.InstanceID, startTime, stopTime)
					s.scheduleDetail(cfg, chatID, s.indexOfInstance(pending.InstanceID), messageID)
					return true
				}
			}
		}
	}
	s.display(cfg, chatID,
		fmt.Sprintf("定时计划保存失败: %v\n\n请重新发送，例如：08:30 23:15", err),
		map[string]any{"inline_keyboard": []any{
			[]any{map[string]string{"text": "取消修改", "callback_data": "ag:schedule"}},
		}}, messageID)
	return true
}

// saveScheduleTimes 在 cycle 鎖 + config 鎖內校驗並寫入新的排程時間。
func (s *service) saveScheduleTimes(instanceID, startTime, stopTime string) error {
	release, locked := guard.AcquireCycleLockPublic(s.paths)
	if !locked {
		return fmt.Errorf("检测任务正在运行，请稍后重新输入")
	}
	defer release()
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		return fmt.Errorf("配置读取失败: %v", err)
	}
	index := -1
	for i, user := range config.Users(cfg) {
		if config.GetStr(user, "instance_id") == instanceID {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("实例不存在或配置已经变化")
	}
	user := config.Users(cfg)[index]
	schedule := guard.GetScheduleConfig(user)
	user["schedule"] = map[string]any{
		"enabled":    schedule["enabled"],
		"start_time": startTime,
		"stop_time":  stopTime,
	}
	if err := config.Save(s.paths.Config, cfg); err != nil {
		return fmt.Errorf("配置写入失败: %v", err)
	}
	return nil
}

// confirmScheduleToggle 對應 _confirm_schedule_toggle()。
func (s *service) confirmScheduleToggle(cfg map[string]any, chatID int64, index int, messageID int64) {
	users := config.Users(mustLoadConfig(s.paths))
	if index < 0 || index >= len(users) {
		s.scheduleChoices(cfg, chatID, messageID)
		return
	}
	user := users[index]
	schedule := guard.GetScheduleConfig(user)
	enabled := schedule["enabled"] != true
	action := "关闭"
	if enabled {
		action = "启用"
	}
	effect := "关闭后不会立即改变实例当前状态。"
	if enabled {
		effect = "启用后，后台会在 1 分钟内按当前时段执行计划。"
	}
	text := fmt.Sprintf("确认%s定时计划？\n\n实例: %s (%s)\n每日开机: %v\n每日关机: %v\n\n%s",
		action, orDefault(config.GetStr(user, "name"), config.GetStr(user, "instance_id")),
		config.GetStr(user, "instance_id"),
		schedule["start_time"], schedule["stop_time"], effect)
	setValue := "0"
	if enabled {
		setValue = "1"
	}
	s.display(cfg, chatID, text, map[string]any{"inline_keyboard": []any{
		[]any{
			map[string]string{"text": "确认" + action, "callback_data": fmt.Sprintf("ag:sched:set:%d:%s", index, setValue)},
			map[string]string{"text": "取消", "callback_data": fmt.Sprintf("ag:sched:view:%d", index)},
		},
	}}, messageID)
}

// setScheduleEnabled 對應 _set_schedule_enabled()：cycle 鎖 + config 鎖內寫入。
func (s *service) setScheduleEnabled(cfg map[string]any, chatID, userID int64, index int, enabled bool, messageID int64) error {
	release, locked := guard.AcquireCycleLockPublic(s.paths)
	if !locked {
		return fmt.Errorf("检测任务正在运行，请稍后再试")
	}
	defer release()
	guard.ConfigLock()
	defer guard.ConfigUnlock()
	cfgData, err := config.Load(s.paths.Config)
	if err != nil {
		return fmt.Errorf("配置读取失败: %v", err)
	}
	users := config.Users(cfgData)
	if index < 0 || index >= len(users) {
		return fmt.Errorf("实例不存在或配置已经变化")
	}
	user := users[index]
	schedule := guard.GetScheduleConfig(user)
	user["schedule"] = map[string]any{
		"enabled":    enabled,
		"start_time": schedule["start_time"],
		"stop_time":  schedule["stop_time"],
	}
	if err := config.Save(s.paths.Config, cfgData); err != nil {
		return fmt.Errorf("配置写入失败: %v", err)
	}
	log.Printf("Telegram 管理员 %d %s实例 %s 定时计划",
		userID, map[bool]string{true: "启用", false: "关闭"}[enabled], config.GetStr(user, "instance_id"))
	s.scheduleDetail(cfg, chatID, index, messageID)
	return nil
}

// --- 授權與訊息/回調處理 ---

// authorized 對應 _authorized()：私聊限定（allow_group_control 擴充除外）+
// 管理員白名單。通過時回傳 (userID, chatID, true)。
func (s *service) authorized(tg map[string]any, admins map[int64]bool, source map[string]any, callbackID string) (int64, int64, bool) {
	userID, _ := toFloat(nested(source, "from")["id"])
	chat := nested(source, "chat")
	if chat["type"] == nil {
		chat = nested(source, "message", "chat")
	}
	chatID, hasChat := toFloat(chat["id"])
	allowGroup := config.GetBool(tg, "allow_group_control", false) // Go 擴充欄位
	if fmt.Sprintf("%v", chat["type"]) != "private" && !allowGroup {
		log.Printf("Telegram Bot 控制忽略非私聊命令: %v", chat["id"])
		if callbackID != "" {
			s.answerCallback(tg, callbackID, "仅支持私聊", true)
		}
		return 0, 0, false
	}
	if !admins[int64(userID)] {
		log.Printf("Telegram Bot 控制拒绝未授权用户: %v", userID)
		if callbackID != "" {
			s.answerCallback(tg, callbackID, "无权限", true)
		} else if hasChat {
			s.send(tgToCfg(tg), int64(chatID), "无权限。", nil)
		}
		return 0, 0, false
	}
	return int64(userID), int64(chatID), true
}

func (s *service) handleMessage(cfg, tg map[string]any, admins map[int64]bool, message map[string]any) {
	userID, chatID, ok := s.authorized(tg, admins, message, "")
	if !ok {
		return
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", message["text"]))
	if text == "<nil>" {
		text = ""
	}
	if s.handleScheduleInput(cfg, userID, chatID, text) {
		return
	}
	if !strings.HasPrefix(text, "/") {
		s.sendHelp(cfg, chatID, 0)
		return
	}
	parts := strings.Fields(text)
	command := strings.ToLower(strings.SplitN(parts[0], "@", 2)[0])
	// 群組中指向其他 bot 的指令（@xxx 後綴不匹配本 bot）由 allow_group_control
	// 場景過濾：botUsername 於 prepareToken 時記錄，於此比對
	if at := strings.Index(parts[0], "@"); at >= 0 && s.botUsername != "" {
		if !strings.EqualFold(parts[0][at+1:], s.botUsername) {
			return
		}
	}
	argument := strings.TrimSpace(strings.Join(parts[1:], " "))

	switch {
	case command == "/start" || command == "/help" || command == "/menu":
		s.sendHelp(cfg, chatID, 0)
	case command == "/status":
		s.showStatus(cfg, chatID, 0)
	case command == "/instances" || command == "/list":
		s.showInstances(cfg, chatID, 0)
	case command == "/check":
		s.newConfirmation(cfg, chatID, userID, "check", "", 1, false, 0, 0, 0)
	case command == "/schedule" || command == "/plan":
		if argument == "" {
			s.scheduleChoices(cfg, chatID, 0)
			return
		}
		index, _ := resolveInstance(config.Users(mustLoadConfig(s.paths)), argument)
		if index < 0 {
			s.send(cfg, chatID, "实例不存在，请使用 /instances 查看序号。", viewMarkup("ag:schedule"))
			return
		}
		s.scheduleDetail(cfg, chatID, index, 0)
	case command == "/poweron" || command == "/on" || command == "/poweroff" || command == "/off":
		action := "stop"
		if command == "/poweron" || command == "/on" {
			action = "start"
		}
		if argument == "" {
			s.instanceChoices(cfg, chatID, action, 0)
			return
		}
		index, _ := resolveInstance(config.Users(mustLoadConfig(s.paths)), argument)
		if index < 0 {
			s.send(cfg, chatID, "实例不存在，请使用 /instances 查看序号。", nil)
			return
		}
		instanceID := config.GetStr(config.Users(mustLoadConfig(s.paths))[index], "instance_id")
		s.newConfirmation(cfg, chatID, userID, action, instanceID, 1, false, 0, 0, 0)
	default:
		s.sendHelp(cfg, chatID, 0)
	}
}

// handleCallback 對應 _handle_callback()：ag:* 回調路由。
func (s *service) handleCallback(cfg, tg map[string]any, admins map[int64]bool, callback map[string]any) {
	callbackID, _ := callback["id"].(string)
	userID, chatID, ok := s.authorized(tg, admins, callback, callbackID)
	if !ok {
		return
	}
	_, messageID := callbackMessageRef(callback)
	data, _ := callback["data"].(string)
	delete(s.scheduleInputs, userID)
	if !strings.HasPrefix(data, "ag:confirm:") && !strings.HasPrefix(data, "ag:cancel:") {
		for token, item := range s.pending {
			if item.UserID == userID {
				delete(s.pending, token)
			}
		}
	}
	switch {
	case data == "ag:menu":
		s.answerCallback(tg, callbackID, "", false)
		s.sendHelp(cfg, chatID, messageID)
	case data == "ag:close":
		s.answerCallback(tg, callbackID, "菜单已关闭", false)
		s.closeMenu(tg, callback)
	case data == "ag:status":
		s.answerCallback(tg, callbackID, "", false)
		s.showStatus(cfg, chatID, messageID)
	case data == "ag:instances":
		s.answerCallback(tg, callbackID, "", false)
		s.showInstances(cfg, chatID, messageID)
	case data == "ag:schedule":
		s.answerCallback(tg, callbackID, "", false)
		s.scheduleChoices(cfg, chatID, messageID)
	case strings.HasPrefix(data, "ag:sched:view:"):
		index, ok := parseTrailingInt(data)
		if !ok {
			s.answerCallback(tg, callbackID, "实例序号无效", true)
			return
		}
		s.answerCallback(tg, callbackID, "", false)
		s.scheduleDetail(cfg, chatID, index, messageID)
	case strings.HasPrefix(data, "ag:sched:edit:"):
		index, ok := parseTrailingInt(data)
		if !ok {
			s.answerCallback(tg, callbackID, "实例序号无效", true)
			return
		}
		s.answerCallback(tg, callbackID, "", false)
		s.promptScheduleInput(cfg, chatID, userID, index, messageID)
	case strings.HasPrefix(data, "ag:sched:ask:"):
		index, ok := parseTrailingInt(data)
		if !ok {
			s.answerCallback(tg, callbackID, "实例序号无效", true)
			return
		}
		s.answerCallback(tg, callbackID, "", false)
		s.confirmScheduleToggle(cfg, chatID, index, messageID)
	case strings.HasPrefix(data, "ag:sched:set:"):
		parts := strings.Split(data, ":")
		if len(parts) != 5 || (parts[4] != "0" && parts[4] != "1") {
			s.answerCallback(tg, callbackID, "计划操作无效", true)
			return
		}
		index, ok := parseAllDigits(parts[3])
		if !ok {
			s.answerCallback(tg, callbackID, "实例序号无效", true)
			return
		}
		s.answerCallback(tg, callbackID, "正在保存", false)
		if err := s.setScheduleEnabled(cfg, chatID, userID, index, parts[4] == "1", messageID); err != nil {
			s.display(cfg, chatID, fmt.Sprintf("定时计划保存失败: %v", err), viewMarkup("ag:schedule"), messageID)
		}
	case data == "ag:req:check":
		s.answerCallback(tg, callbackID, "", false)
		s.newConfirmation(cfg, chatID, userID, "check", "", 1, false, 0, 0, messageID)
	case strings.HasPrefix(data, "ag:list:"):
		action := data[strings.LastIndex(data, ":")+1:]
		s.answerCallback(tg, callbackID, "", false)
		if action == "start" || action == "stop" {
			s.instanceChoices(cfg, chatID, action, messageID)
		}
	case strings.HasPrefix(data, "ag:req:"):
		parts := strings.SplitN(data, ":", 4)
		s.answerCallback(tg, callbackID, "", false)
		if len(parts) == 4 && (parts[2] == "start" || parts[2] == "stop") {
			index, ok := parseAllDigits(parts[3])
			users := config.Users(mustLoadConfig(s.paths))
			if !ok || index >= len(users) {
				s.display(cfg, chatID, "实例不存在或配置已经变化。", menuMarkup(), messageID)
				return
			}
			s.newConfirmation(cfg, chatID, userID, parts[2],
				config.GetStr(users[index], "instance_id"), 1, false, 0, 0, messageID)
		}
	case strings.HasPrefix(data, "ag:cancel:"):
		token := data[len("ag:cancel:"):]
		if pending, exists := s.pending[token]; exists && pending.UserID == userID {
			delete(s.pending, token)
			s.answerCallback(tg, callbackID, "已取消", false)
			s.sendHelp(cfg, chatID, messageID)
		} else {
			s.answerCallback(tg, callbackID, "操作已失效", true)
		}
	case strings.HasPrefix(data, "ag:confirm:"):
		token := data[len("ag:confirm:"):]
		s.expirePending()
		pending, exists := s.pending[token]
		if !exists || pending.UserID != userID || pending.ChatID != chatID {
			s.answerCallback(tg, callbackID, "确认已过期或无效", true)
			return
		}
		delete(s.pending, token)
		pending.MessageID = messageID
		s.answerCallback(tg, callbackID, "正在执行", false)
		s.executePending(cfg, pending)
	default:
		s.answerCallback(tg, callbackID, "按钮无效", true)
	}
}

func (s *service) handleUpdate(cfg, tg map[string]any, admins map[int64]bool, update map[string]any) {
	if msg, ok := update["message"].(map[string]any); ok {
		s.handleMessage(cfg, tg, admins, msg)
	} else if cb, ok := update["callback_query"].(map[string]any); ok {
		s.handleCallback(cfg, tg, admins, cb)
	}
}

// --- Token 指紋與 offset 持久化（對應 _prepare_token/_load_offset/_save_offset）---

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])[:24]
}

func (s *service) controlStatePath() string {
	if p := os.Getenv("ALIYUN_GUARD_TELEGRAM_CONTROL_STATE"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(s.paths.Config), "telegram-control-state.json")
}

func (s *service) loadOffset() *int64 {
	raw, err := os.ReadFile(s.controlStatePath())
	if err != nil {
		return nil
	}
	var data struct {
		TokenFingerprint string `json:"token_fingerprint"`
		Offset           int64  `json:"offset"`
	}
	if json.Unmarshal(raw, &data) != nil || data.TokenFingerprint != s.fingerprint || data.Offset < 0 {
		return nil
	}
	return &data.Offset
}

func (s *service) saveOffset() {
	if s.offset == nil {
		return
	}
	data, _ := json.Marshal(map[string]any{
		"token_fingerprint": s.fingerprint,
		"offset":            *s.offset,
	})
	p := s.controlStatePath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, data, 0o600)
}

// prepareToken 對應 _prepare_token()：Token 變更或 drainPending 時丟棄積壓
// 訊息（offset=latest+1），並於 Token 變更時註冊命令選單。
func (s *service) prepareToken(tg map[string]any) error {
	fingerprint := tokenFingerprint(config.GetStr(tg, "bot_token"))
	tokenChanged := fingerprint != s.fingerprint
	if !tokenChanged && s.offset != nil && !s.drainPending {
		return nil
	}
	if tokenChanged {
		s.drainPending = true
	}
	s.fingerprint = fingerprint
	if s.drainPending {
		s.offset = nil
	} else {
		s.offset = s.loadOffset()
	}
	if s.offset == nil {
		updates, err := s.tgAPIRaw(tg, "getUpdates", map[string]any{
			"offset":          -1,
			"limit":           1,
			"timeout":         0,
			"allowed_updates": []string{"message", "callback_query"},
		})
		if err != nil {
			return err
		}
		offset := int64(0)
		if len(updates) > 0 {
			for _, u := range updates {
				if id, ok := toFloat(asMap(u)["update_id"]); ok && int64(id) > offset-1 {
					offset = int64(id)
				}
			}
			offset++
		}
		s.offset = &offset
		s.saveOffset()
		log.Printf("Telegram Bot 控制已丢弃启用前的待处理消息")
	}
	s.drainPending = false
	if s.commandsFingerprint != fingerprint {
		// setMyCommands 註冊私聊命令選單（對應 BOT_COMMANDS）
		payload := map[string]any{
			"commands": botCommands,
			"scope":    map[string]any{"type": "all_private_chats"},
		}
		if _, err := s.tgAPI(tg, "setMyCommands", payload, false); err != nil {
			log.Printf("Telegram Bot 命令菜单注册失败，文本命令仍可使用: %v", err)
		}
		s.commandsFingerprint = fingerprint
	}
	// 記錄自身 username（allow_group_control 群組場景過濾 @ 後綴用）
	if me, err := s.tgAPI(tg, "getMe", map[string]any{}, false); err == nil {
		s.botUsername, _ = me["username"].(string)
	}
	return nil
}

// --- 鉤子預設實作（真實 API 呼叫，測試可替換）---

func (s *service) runCycle(cfg map[string]any) string {
	if s.runCycleFn != nil {
		return s.runCycleFn(s.paths, true)
	}
	release, locked := guard.AcquireCycleLockPublic(s.paths)
	if !locked {
		return "已有检测任务正在运行，请稍后再试。"
	}
	defer release()
	guard.RunCyclePublic(s.paths, false, true)
	st, err := state.Load(s.paths.State)
	if err != nil {
		return "检测已完成。"
	}
	summary, _ := st["last_summary"].(string)
	if summary == "" {
		return "检测已完成。"
	}
	return summary
}

func (s *service) controlInstance(action string, index int, thresholdOverride bool) (string, error) {
	if s.controlFn != nil {
		return s.controlFn(s.paths, index, action)
	}
	result, err := guard.ControlInstance(s.paths, index, action, "Telegram Bot",
		false, thresholdOverride, true)
	if err != nil {
		return "", err
	}
	return result.Message, nil
}

// queryStatusAndTraffic 在 cycle 鎖內複查實例狀態與 CDT 流量（二次開機確認用）。
func (s *service) queryStatusAndTraffic(instanceID string) (string, float64, float64, error) {
	if s.queryStatusFn != nil {
		return s.queryStatusFn(s.paths, s.indexOfInstance(instanceID))
	}
	release, locked := guard.AcquireCycleLockPublic(s.paths)
	if !locked {
		return "", 0, 0, fmt.Errorf("已有检测任务正在运行，请稍后重新操作。")
	}
	defer release()
	index := s.indexOfInstance(instanceID)
	if index < 0 {
		return "", 0, 0, fmt.Errorf("实例不存在或配置已经变化。")
	}
	user := config.Users(mustLoadConfig(s.paths))[index]
	status, err := guard.QueryInstanceStatusPublic(user)
	if err != nil {
		return "", 0, 0, err
	}
	traffic, err := guard.QueryTrafficPublic(user)
	if err != nil {
		return "", 0, 0, err
	}
	return status, traffic, config.GetFloat(user, "traffic_limit_gb", 0), nil
}

func (s *service) indexOfInstance(instanceID string) int {
	users := config.Users(mustLoadConfig(s.paths))
	for i, user := range users {
		if config.GetStr(user, "instance_id") == instanceID {
			return i
		}
	}
	return -1
}

// --- 小工具 ---

func mustLoadConfig(paths config.Paths) map[string]any {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return map[string]any{"users": []any{}}
	}
	return cfg
}

func splitMessage(text string) []string { return notify.SplitMessage(text, 3900) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func formatTraffic(v any) string {
	if f, ok := toFloat(v); ok {
		return fmt.Sprintf("%.2f GB", f)
	}
	return "暂无"
}

func toFloatOr(v any) float64 {
	f, _ := toFloat(v)
	return f
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseAllDigits(s string) (int, bool) {
	if !isAllDigits(s) {
		return 0, false
	}
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n, true
}

func parseTrailingInt(data string) (int, bool) {
	return parseAllDigits(data[strings.LastIndex(data, ":")+1:])
}

func firstOf(parts []string) string {
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func secondOf(parts []string) string {
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}

func tgToCfg(tg map[string]any) map[string]any {
	return map[string]any{"telegram": tg}
}

// logIfChanged 僅在訊息變化時記錄（對應 _log_inactive，避免每輪重複刷日誌）。
func logIfChanged(last *string, message string) {
	if *last != message {
		log.Printf("%s", message)
		*last = message
	}
}

func nested(m map[string]any, path ...string) map[string]any {
	cur := m
	for _, key := range path {
		next, ok := cur[key].(map[string]any)
		if !ok {
			return map[string]any{}
		}
		cur = next
	}
	return cur
}

// asMap 安全斷言 any → map[string]any（失敗回空 map）。
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func toFloat(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}
