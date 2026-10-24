// Package notify 負責 Telegram 通知：直接連線 api.telegram.org（海外主機直連即可，
// sing-box 節點代理模組已按需求放棄，不再支援 connection_mode=proxy/node）。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aliyun-guard-go/internal/config"
)

// SendMessageFunc 為套件級函式變數：測試中替換以攔截送出訊息。
var SendMessageFunc = sendMessageDefault

// SendMessage 發送純文字訊息到配置的 chat_id，帶重試，對應 send_telegram_message()。
func SendMessage(cfg map[string]any, text string) error {
	return SendMessageFunc(cfg, text)
}

// sendMessageDefault 為 SendMessage 的預設實作。
func sendMessageDefault(cfg map[string]any, text string) error {
	tg := config.GetMap(cfg, "telegram")
	return SendMessageTo(cfg, tg["chat_id"], text)
}

// SendMessageTo 發送純文字訊息到指定 chat（chatID 可為 float64/int64/字串），
// 供 Bot 控制回覆指令來源的私聊或群組。
func SendMessageTo(cfg map[string]any, chatID any, text string) error {
	tg := config.GetMap(cfg, "telegram")
	token := config.GetStr(tg, "bot_token")
	if token == "" || chatID == nil || fmt.Sprintf("%v", chatID) == "" {
		return fmt.Errorf("Telegram 未配置 bot_token 或 chat_id")
	}
	base := config.GetStr(tg, "api_base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	timeout := config.GetInt(tg, "timeout_seconds", 12)
	retries := config.GetInt(tg, "retries", 3)
	if retries < 1 {
		retries = 1
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}

	payload, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		resp, err := client.Post(base+"/bot"+token+"/sendMessage", "application/json", bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		var result struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("HTTP %d: 回應非 JSON", resp.StatusCode)
			continue
		}
		if result.OK {
			return nil
		}
		lastErr = fmt.Errorf("Telegram API: %s", result.Description)
	}
	return lastErr
}

// TestMessage 發送測試訊息，對應 test_telegram() 的基本路徑。
func TestMessage(cfg map[string]any) error {
	return SendMessage(cfg, fmt.Sprintf("阿里云保活测试消息\n时间: %s", time.Now().Format("2006-01-02 15:04:05")))
}

// SplitMessage 依上限拆分長訊息（預設 3900，低於 API 4096 限制），對應 split_message()。
func SplitMessage(text string, limit int) []string {
	if limit <= 0 {
		limit = 3900
	}
	if len(text) <= limit {
		return []string{text}
	}
	var parts []string
	var current strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if current.Len()+len(line) > limit && current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// CallAPI 供 Bot 控制模組呼叫其他 Telegram API 方法（getUpdates 等）。
func CallAPI(cfg map[string]any, method string, payload any) (map[string]any, error) {
	tg := config.GetMap(cfg, "telegram")
	token := config.GetStr(tg, "bot_token")
	if token == "" {
		return nil, fmt.Errorf("Telegram 未配置 bot_token")
	}
	base := config.GetStr(tg, "api_base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	timeout := config.GetInt(tg, "timeout_seconds", 12)
	client := &http.Client{Timeout: time.Duration(timeout+20) * time.Second} // 長輪詢需較長逾時
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	resp, err := client.Post(base+"/bot"+token+"/"+method, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	// result 型別依方法而異（物件/布林/陣列），統一以 RawMessage 接收；
	// 非物件結果回空 map。
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("HTTP %d: 回應非 JSON", resp.StatusCode)
	}
	if !envelope.OK {
		return nil, fmt.Errorf("Telegram API %s: %s", method, envelope.Description)
	}
	out := map[string]any{}
	if len(envelope.Result) > 0 {
		_ = json.Unmarshal(envelope.Result, &out)
	}
	return out, nil
}
