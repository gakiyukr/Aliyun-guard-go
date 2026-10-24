// 網路面板 HTTP 伺服器（M3）：對應 web_panel.py 的 PanelHandler 全部路由。
// 認證：PBKDF2 密碼 + HttpOnly Session + CSRF（X-CSRF-Token）；登入限流。
// 變更操作以 mutation 鎖序列化；檢測輪/帳單刷新走單一背景工作槽（job dict）。
// 已裁剪：回滾、程式更新、節點代理路由（s3 備份/加密備份/實例探索已於 M5 補回）。
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/guard"
	"aliyun-guard-go/internal/xtime"
	"aliyun-guard-go/webapp"
)

// minManualCycleInterval 手動檢測最小間隔（節流）。
const minManualCycleInterval = 30 * time.Second

// PanelServer 承載 Session、限流、變更鎖與背景工作狀態。
type PanelServer struct {
	paths    config.Paths
	sessions *sessionStore
	limiter  *loginLimiter
	web      map[string]any

	mutationMu sync.Mutex // 序列化全部寫入操作（對應 mutation_lock）

	jobMu sync.Mutex
	job   map[string]any
	// lastCycleAt 記錄上次手動檢測觸發時間，用於最小間隔節流。
	lastCycleAt time.Time
	// lastBillingAt 記錄上次帳單刷新觸發時間，用於最小間隔節流。
	lastBillingAt time.Time

	restartOnce sync.Once
}

// StartBackground 對應 web_panel.start_background()：未啟用時靜默跳過，
// 啟用時以 goroutine 背景運行（守護模式用）。
func StartBackground(paths config.Paths) {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		guard.Logger.Errorf("網路面板啟動失敗（配置讀取）: %v", err)
		return
	}
	if config.GetMap(cfg, "web_panel") == nil || !config.GetBool(config.GetMap(cfg, "web_panel"), "enabled", false) {
		return
	}
	go func() {
		if err := Serve(paths); err != nil {
			guard.Logger.Printf("網路面板結束: %v", err)
		}
	}()
}

func newJob() map[string]any {
	return map[string]any{"running": false, "started_at": nil, "finished_at": nil, "error": nil}
}

// Serve 啟動網路面板（阻塞）。web_panel.enabled 為 false 時拒絕啟動。
func Serve(paths config.Paths) error {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return err
	}
	if err := validateWebConfig(cfg); err != nil {
		return err
	}
	web := getWebConfig(cfg)
	if web["enabled"] != true {
		return fmt.Errorf("网页面板未启用（web_panel.enabled=false）")
	}
	server := &PanelServer{
		paths:    paths,
		sessions: newSessionStore(),
		limiter:  newLoginLimiter(),
		web:      web,
		job:      newJob(),
	}
	addr := fmt.Sprintf("%s:%d", web["host"], web["port"])
	guard.Logger.Printf("网页控制面板已启动: http://%s", addr)
	return http.ListenAndServe(addr, server.routes())
}

func (s *PanelServer) routes() http.Handler {
	mux := http.NewServeMux()

	// GET 路由
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": appVersion})
	})
	// SPA 靜態資源 fallback（/api、/healthz 等註冊較晚但 Go mux 以最長
	// 模式優先，不受此通配影響）
	mux.HandleFunc("GET /", s.serveWebapp)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/dashboard", s.auth(s.handleDashboard))
	mux.HandleFunc("GET /api/management", s.auth(s.handleManagement))
	mux.HandleFunc("GET /api/logs", s.auth(s.handleLogs))
	mux.HandleFunc("GET /api/job", s.auth(s.handleJob))

	// POST 路由（login 免認證，其餘需 Session + CSRF，並以 mutation 鎖序列化）
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.post(s.handleLogout))
	mux.HandleFunc("POST /api/run", s.post(s.handleRun))
	mux.HandleFunc("POST /api/billing/refresh", s.post(s.handleBillingRefresh))
	mux.HandleFunc("POST /api/settings", s.post(s.handleSettings))
	mux.HandleFunc("POST /api/web-settings", s.post(s.handleWebSettings))
	mux.HandleFunc("POST /api/telegram/identity", s.post(s.handleTelegramIdentity))
	mux.HandleFunc("POST /api/telegram/test", s.post(s.handleTelegramTest))
	mux.HandleFunc("POST /api/instances", s.post(s.handleInstanceSave))
	mux.HandleFunc("POST /api/instances/{index}", s.post(s.handleInstanceSave))
	mux.HandleFunc("POST /api/instances/{index}/validate", s.post(s.handleInstanceValidate))
	mux.HandleFunc("POST /api/instances/{index}/delete", s.post(s.handleInstanceDelete))
	mux.HandleFunc("POST /api/instances/{index}/schedule", s.post(s.handleInstanceSchedule))
	mux.HandleFunc("POST /api/instances/{index}/pause", s.post(s.handleInstancePause))
	mux.HandleFunc("POST /api/instances/{index}/logging", s.post(s.handleInstanceLogging))
	mux.HandleFunc("POST /api/instances/{index}/power", s.post(s.handleInstancePower))
	mux.HandleFunc("POST /api/service/restart", s.post(s.handleServiceRestart))

	// M5：加密備份 / S3 備份 / 實例探索（對應 web_panel.py 同名路由）
	mux.HandleFunc("POST /api/backup/create", s.post(s.handleBackupCreate))
	mux.HandleFunc("POST /api/backup/preview", s.post(s.handleBackupPreview))
	mux.HandleFunc("POST /api/backup/restore", s.post(s.handleBackupRestore))
	mux.HandleFunc("POST /api/s3-backup/settings", s.post(s.handleS3Settings))
	mux.HandleFunc("POST /api/s3-backup/test", s.post(s.handleS3Test))
	mux.HandleFunc("POST /api/s3-backup/list", s.post(s.handleS3List))
	mux.HandleFunc("POST /api/s3-backup/run", s.post(s.handleS3Run))
	mux.HandleFunc("POST /api/s3-backup/preview", s.post(s.handleS3Preview))
	mux.HandleFunc("POST /api/s3-backup/restore", s.post(s.handleS3Restore))
	mux.HandleFunc("POST /api/discovery/scan", s.post(s.handleDiscoveryScan))
	mux.HandleFunc("POST /api/discovery/import", s.post(s.handleDiscoveryImport))

	return mux
}

// --- 基礎輔助 ---

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

// writeResult 統一輸出 {"ok": true, ...}；PanelError 映射狀態碼。
func writeResult(w http.ResponseWriter, fields map[string]any, err error) {
	if err != nil {
		var panelErr *PanelError
		status := 500
		message := "服务器内部错误"
		if errors.As(err, &panelErr) {
			status = panelErr.Status
			message = panelErr.Message
		}
		writeJSON(w, status, map[string]any{"ok": false, "error": message})
		return
	}
	payload := map[string]any{"ok": true}
	for key, value := range fields {
		payload[key] = value
	}
	writeJSON(w, http.StatusOK, payload)
}

// auth 包裝僅需登入的 GET 路由。
func (s *PanelServer) auth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.authenticated(w, r, false); !ok {
			return // authenticated 已輸出 401
		}
		next(w, r)
	}
}

// post 包裝變更操作：Session + CSRF 驗證後以 mutation 鎖序列化執行。
func (s *PanelServer) post(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.authenticated(w, r, true); !ok {
			return // authenticated 已輸出 401/403
		}
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		next(w, r)
	}
}

func (s *PanelServer) readJSON(r *http.Request) (map[string]any, error) {
	data := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil && !errors.Is(err, io.EOF) {
		return nil, &PanelError{"请求体不是有效的 JSON", 400}
	}
	return data, nil
}

func pathIndex(r *http.Request) (int, error) {
	raw := r.PathValue("index")
	index, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &PanelError{"实例序号无效", 400}
	}
	return index, nil
}

// --- GET 處理 ---

// serveWebapp 服務 Nuxt SPA 靜態檔案：/ 回 index.html（no-store），
// /_nuxt/* hashed 資源帶 immutable 快取；未命中路徑回 index.html（SPA
// history 模式 fallback）。/
func (s *PanelServer) serveWebapp(w http.ResponseWriter, r *http.Request) {
	// mux 已保證方法為 GET
	raw := r.URL.Path
	if raw == "/" {
		raw = "/index.html"
	}
	clean := path.Clean(raw)
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	data, err := webapp.Dist.ReadFile("dist" + clean)
	if err != nil {
		// SPA fallback：未命中回 index.html
		data, err = webapp.Dist.ReadFile("dist/index.html")
		if err != nil {
			http.Error(w, "面板資源缺失（webapp/dist 未建置）", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
		return
	}
	if strings.HasPrefix(clean, "/_nuxt/") {
		// hashed 資源可永久快取
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, path.Base(clean), time.Time{}, strings.NewReader(string(data)))
}

func (s *PanelServer) handleSession(w http.ResponseWriter, r *http.Request) {
	id := s.sessionID(r)
	data, ok := s.sessions.get(id)
	payload := map[string]any{
		"authenticated": ok,
		"csrf":          nil,
		"version":       appVersion,
		"secure_cookie": s.browserUsesHTTPS(r),
	}
	if ok {
		payload["csrf"] = data.CSRF
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *PanelServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.jobMu.Lock()
	job := s.job
	s.jobMu.Unlock()
	payload, err := dashboardPayload(s.paths, job)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *PanelServer) handleManagement(w http.ResponseWriter, r *http.Request) {
	payload, err := managementPayload(s.paths, "go")
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *PanelServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	payload, err := logsPayload(s.paths, limit, r.URL.Query().Get("instance"))
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *PanelServer) handleJob(w http.ResponseWriter, r *http.Request) {
	s.jobMu.Lock()
	job := s.job
	s.jobMu.Unlock()
	writeJSON(w, http.StatusOK, job)
}

// --- POST 處理 ---

func (s *PanelServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	address := clientAddress(r)
	if !s.limiter.allowed(address) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "登录失败次数过多，请 5 分钟后再试"})
		return
	}
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	cfg, err := config.Load(s.paths.Config)
	if err != nil {
		writeResult(w, nil, &PanelError{fmt.Sprintf("配置读取失败: %v", err), 500})
		return
	}
	web := getWebConfig(cfg)
	usernameOK := constantTimeString(data["username"], web["username"])
	passwordOK := verifyPassword(toString(data["password"]), web["password_hash"].(string))
	if !usernameOK || !passwordOK {
		s.limiter.record(address)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "用户名或密码错误"})
		return
	}
	s.limiter.clear(address)
	id, session := s.sessions.create()
	http.SetCookie(w, s.sessionCookie(id, sessionSeconds))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "csrf": session.CSRF, "secure_cookie": s.browserUsesHTTPS(r),
	})
}

func (s *PanelServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.delete(s.sessionID(r))
	http.SetCookie(w, s.sessionCookie("", 0))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *PanelServer) handleRun(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	dryRun, ok := data["dry_run"].(bool)
	if !ok {
		dryRun = false
	}
	job, jobErr := s.startCycleJob(dryRun)
	if jobErr != nil {
		writeResult(w, nil, jobErr)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "job": job})
}

func (s *PanelServer) handleBillingRefresh(w http.ResponseWriter, r *http.Request) {
	if _, err := s.readJSON(r); err != nil {
		writeResult(w, nil, err)
		return
	}
	job, jobErr := s.startBillingJob()
	if jobErr != nil {
		writeResult(w, nil, jobErr)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "job": job})
}

func (s *PanelServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := updateGlobalSettings(s.paths, data)
	writeResult(w, map[string]any{"settings": result}, err)
}

func (s *PanelServer) handleWebSettings(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := updateWebSettings(s.paths, data)
	if err == nil && result["restart_required"] == true {
		s.delayedRestart()
	}
	writeResult(w, map[string]any{"web": result}, err)
}

func (s *PanelServer) handleTelegramIdentity(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := updateTelegramIdentity(s.paths, data)
	writeResult(w, map[string]any{"telegram": result}, err)
}

func (s *PanelServer) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	result, err := testCurrentTelegram(s.paths)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleInstanceSave(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	var index *int
	if raw := r.PathValue("index"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeResult(w, nil, &PanelError{"实例序号无效", 400})
			return
		}
		index = &parsed
	}
	result, err := saveInstance(s.paths, data, index)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleInstanceValidate(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := validateInstance(s.paths, index)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleInstanceDelete(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := deleteInstance(s.paths, index, toString(data["instance_id"]))
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleInstanceSchedule(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := updateSchedule(s.paths, index, data)
	writeResult(w, map[string]any{"schedule": result}, err)
}

func (s *PanelServer) handleInstancePause(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := updatePause(s.paths, index, data["paused"])
	writeResult(w, map[string]any{"paused": result}, err)
}

func (s *PanelServer) handleInstanceLogging(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := updateInstanceLogging(s.paths, index, data["enabled"])
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleInstancePower(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	action := toString(data["action"])
	result, err := controlInstanceForPanel(s.paths, index, action)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	s.delayedRestart()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "后台服务重启已安排"})
}

// --- 背景工作槽（對應 start_cycle_job / start_billing_job）---

func (s *PanelServer) jobRunning() bool {
	return s.job["running"] == true
}

func (s *PanelServer) startCycleJob(dryRun bool) (map[string]any, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if s.jobRunning() {
		return nil, &PanelError{"检测任务已经在运行", 409}
	}
	// 最小間隔節流（借鑑上游 spot_ddns「觸發過於頻繁跳過本次」防爆倉模式）：
	// 30 秒內重複觸發直接拒絕，避免連點打爆阿里雲 API 限流。
	if wait := minManualCycleInterval - time.Since(s.lastCycleAt); wait > 0 {
		return nil, &PanelError{fmt.Sprintf("检测触发过于频繁，请 %d 秒后再试", int(wait.Seconds())+1), 429}
	}
	s.lastCycleAt = time.Now()
	s.job = map[string]any{
		"running": true, "started_at": nowStamp(), "finished_at": nil,
		"error": nil, "dry_run": dryRun,
	}
	go func() {
		var jobErr error
		release, locked := guard.AcquireCycleLockPublic(s.paths)
		if !locked {
			jobErr = &PanelError{"其他检测任务正在运行", 409}
		} else {
			guard.RunCyclePublic(s.paths, dryRun, false)
			release()
		}
		s.finishJob(jobErr, nil)
	}()
	return s.job, nil
}

func (s *PanelServer) startBillingJob() (map[string]any, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if s.jobRunning() {
		return nil, &PanelError{"已有后台任务正在运行", 409}
	}
	// 與手動檢測同一節流常數：30 秒內重複觸發直接拒絕，防止連點打爆 API 限流。
	if wait := minManualCycleInterval - time.Since(s.lastBillingAt); wait > 0 {
		return nil, &PanelError{fmt.Sprintf("账单刷新触发过于频繁，请 %d 秒后再试", int(wait.Seconds())+1), 429}
	}
	s.lastBillingAt = time.Now()
	s.job = map[string]any{
		"running": true, "started_at": nowStamp(), "finished_at": nil,
		"error": nil, "type": "billing",
	}
	go func() {
		result := guard.RefreshBillingCachePublic(s.paths)
		var jobErr error
		if result["ok"] != true {
			if msg, ok := result["error"].(string); ok {
				jobErr = &PanelError{msg, 500}
			}
		}
		s.finishJob(jobErr, result)
	}()
	return s.job, nil
}

func (s *PanelServer) finishJob(jobErr error, result map[string]any) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	s.job["running"] = false
	s.job["finished_at"] = nowStamp()
	if jobErr != nil {
		s.job["error"] = jobErr.Error()
	} else {
		s.job["error"] = nil
	}
	if result != nil {
		s.job["result"] = result
	}
}

// --- 重啟（對應 delayed_restart → service_command("restart")）---

// delayedRestart 延遲 600ms 後執行服務重啟：實際分支見 restart_linux.go /
// restart_other.go（容器模式、control.sh、systemd 回退；非 Linux 僅記錄日誌）。
func (s *PanelServer) delayedRestart() {
	s.restartOnce.Do(func() {
		time.AfterFunc(600*time.Millisecond, func() {
			if err := serviceRestart(filepath.Dir(s.paths.Config)); err != nil {
				guard.Logger.Errorf("Web requested restart failed: %v", err)
			}
		})
	})
}

// --- 其他輔助 ---

// controlInstanceForPanel 對應面板 power 路由：通知開啟、允許閾值覆寫關閉。
func controlInstanceForPanel(paths config.Paths, index int, action string) (map[string]any, error) {
	result, err := guard.ControlInstance(paths, index, action, "网页控制台", true, false, false)
	if err != nil {
		status := 502
		var manualErr *guard.ManualError
		if errors.As(err, &manualErr) {
			status = 409
		}
		return nil, &PanelError{err.Error(), status}
	}
	return map[string]any{
		"before": result.Before, "after": result.After, "message": result.Message,
		"threshold_overridden": result.ThresholdOverridden,
		"monitor_paused":       result.MonitorPaused,
		"monitor_resumed":      result.MonitorResumed,
		"notification_error":   nil,
	}, nil
}

// browserUsesHTTPS 對應 _browser_uses_https()：反向代理/來源判斷。
func (s *PanelServer) browserUsesHTTPS(r *http.Request) bool {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if first := proto; first == "https" || strings.HasPrefix(first, "https,") {
			return true
		}
	}
	for _, name := range []string{"Origin", "Referer"} {
		if value := r.Header.Get(name); len(value) > 8 && value[:8] == "https://" {
			return true
		}
	}
	return s.web["cookie_secure"] == true
}

func clientAddress(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		for i := 0; i < len(fwd); i++ {
			if fwd[i] == ',' {
				return fwd[:i]
			}
		}
		return fwd
	}
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			host = host[:i]
			break
		}
	}
	return host
}

// constantTimeString 恆定時間比較 any（nil 安全）。
func constantTimeString(a, b any) bool {
	return constantTimeEqual([]byte(toString(a)), []byte(toString(b)))
}

func nowStamp() string {
	return time.Now().Format(xtime.ISOFormat)
}
