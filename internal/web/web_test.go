// M3 網路面板測試：密碼雜湊互通、Session 過期、登入限流、排程/暫停更新、
// 登入→CSRF→變更操作的 HTTP 全鏈路。
package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliyun-guard-go/internal/config"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := hashPassword("s3cret-pass")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2_sha256$260000$") {
		t.Fatalf("雜湊格式不符: %s", hash)
	}
	if !verifyPassword("s3cret-pass", hash) {
		t.Fatal("正確密碼應驗證通過")
	}
	if verifyPassword("wrong-pass", hash) {
		t.Fatal("錯誤密碼不應驗證通過")
	}
}

func TestSessionExpiry(t *testing.T) {
	store := newSessionStore()
	id, _ := store.create()
	if _, ok := store.get(id); !ok {
		t.Fatal("新建 Session 應可用")
	}
	// 直接改寫過期時間模擬滑動過期失效
	store.mu.Lock()
	data := store.sessions[id]
	data.Expires = time.Now().Add(-time.Second)
	store.sessions[id] = data
	store.mu.Unlock()
	if _, ok := store.get(id); ok {
		t.Fatal("過期 Session 不應可用")
	}
}

func TestLoginLimiter(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < 5; i++ {
		if !limiter.allowed("1.2.3.4") {
			t.Fatalf("第 %d 次失敗前應允許嘗試", i+1)
		}
		limiter.record("1.2.3.4")
	}
	if limiter.allowed("1.2.3.4") {
		t.Fatal("5 次失敗後應拒絕")
	}
	if !limiter.allowed("5.6.7.8") {
		t.Fatal("其他 IP 不應受影響")
	}
	limiter.clear("1.2.3.4")
	if !limiter.allowed("1.2.3.4") {
		t.Fatal("清除後應重新允許")
	}
}

func writeTestConfig(t *testing.T, cfg map[string]any) config.Paths {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Paths{Config: path, State: filepath.Join(dir, "state.json")}
}

func webTestConfig() map[string]any {
	hash, _ := hashPassword("s3cret-pass")
	return map[string]any{
		"telegram": map[string]any{"bot_token": "123:abc", "chat_id": 10086},
		"users":    []any{},
		"web_panel": map[string]any{
			"enabled": true, "host": "127.0.0.1", "port": 18999,
			"username": "admin", "password_hash": hash,
		},
	}
}

// TestScheduleAndPauseUpdate 驗證排程與暫停更新的寫入路徑。
func TestScheduleAndPauseUpdate(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	cfg, _ := config.Load(paths.Config)
	cfg["users"] = []any{map[string]any{
		"name": "HK-CDT", "ak": "a", "sk": "b", "region": "cn-hongkong",
		"instance_id": "i-1", "traffic_limit_gb": 180.0,
	}}
	_ = config.Save(paths.Config, cfg)

	schedule, err := updateSchedule(paths, 0, map[string]any{
		"enabled": true, "start_time": "09:00", "stop_time": "22:00",
	})
	if err != nil {
		t.Fatalf("updateSchedule: %v", err)
	}
	if schedule["start_time"] != "09:00" || schedule["stop_time"] != "22:00" {
		t.Fatalf("排程時間不符: %v", schedule)
	}
	cfg, _ = config.Load(paths.Config)
	saved := config.GetMap(config.Users(cfg)[0], "schedule")
	if saved["enabled"] != true {
		t.Fatal("排程啟用狀態應已持久化")
	}

	paused, err := updatePause(paths, 0, true)
	if err != nil || paused != true {
		t.Fatalf("updatePause: %v %v", paused, err)
	}
	cfg, _ = config.Load(paths.Config)
	if config.GetBool(config.Users(cfg)[0], "paused", false) != true {
		t.Fatal("暫停狀態應已持久化")
	}

	// 相同開關機時間應被拒絕
	if _, err := updateSchedule(paths, 0, map[string]any{
		"enabled": true, "start_time": "09:00", "stop_time": "09:00",
	}); err == nil {
		t.Fatal("相同開關機時間應被拒絕")
	}
}

// TestLoginAndCSRFOverHTTP 全鏈路：登入取得 CSRF → 無 CSRF 被拒 → 帶 CSRF 變更成功。
func TestLoginAndCSRFOverHTTP(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	server := &PanelServer{
		paths:    paths,
		sessions: newSessionStore(),
		limiter:  newLoginLimiter(),
		web:      getWebConfig(webTestConfig()),
		job:      newJob(),
	}
	ts := httptest.NewServer(server.routes())
	defer ts.Close()

	// 未登入存取受保護 API
	resp, err := http.Get(ts.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登入應 401，得到 %d", resp.StatusCode)
	}

	// 錯誤密碼
	body, _ := json.Marshal(map[string]any{"username": "admin", "password": "nope"})
	resp, err = http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("錯誤密碼應 401，得到 %d", resp.StatusCode)
	}

	// 正確登入
	body, _ = json.Marshal(map[string]any{"username": "admin", "password": "s3cret-pass"})
	resp, err = http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&login)
	cookies := resp.Cookies()
	resp.Body.Close()
	if login["ok"] != true || login["csrf"] == nil {
		t.Fatalf("登入應成功並附 CSRF: %v", login)
	}
	if len(cookies) == 0 {
		t.Fatal("登入應設定 Cookie")
	}

	req, _ := http.NewRequest("POST", ts.URL+"/api/run", bytes.NewReader([]byte(`{"dry_run":true}`)))
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF 應 403，得到 %d", resp.StatusCode)
	}

	// 帶 CSRF 的變更操作（背景檢測輪會執行，但空 users 立即完成）
	req, _ = http.NewRequest("POST", ts.URL+"/api/run", bytes.NewReader([]byte(`{"dry_run":true}`)))
	req.Header.Set("X-CSRF-Token", login["csrf"].(string))
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var runResult map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&runResult)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || runResult["ok"] != true {
		t.Fatalf("帶 CSRF 應 202: %d %v", resp.StatusCode, runResult)
	}

	// 等待背景任務結束
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", ts.URL+"/api/job", nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var job map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		if job["running"] != true {
			if job["error"] != nil {
				t.Fatalf("背景任務不應報錯: %v", job["error"])
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("背景任務未在時限內結束")
}
