// M5 網路面板測試：加密備份建立/預覽/還原、S3 設定持久化（不洩漏明文）、
// 實例探索匯入去重，及 discovery/import 的 HTTP 全鏈路。
package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/s3backup"
)

// TestEncryptedBackupFlow 建立 → base64 上傳預覽 → 還原。
func TestEncryptedBackupFlow(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	cfg, _ := config.Load(paths.Config)
	cfg["users"] = []any{map[string]any{
		"name": "HK", "ak": "a", "sk": "b", "region": "cn-hongkong", "instance_id": "i-1",
	}}
	_ = config.Save(paths.Config, cfg)

	result, err := createEncryptedBackup(paths, map[string]any{
		"password": "backup-pass-8", "include_state": true, "include_logs": false,
	})
	if err != nil {
		t.Fatalf("createEncryptedBackup: %v", err)
	}
	if !strings.HasPrefix(result["filename"].(string), "aliyun-guard-") {
		t.Errorf("檔名不符: %v", result["filename"])
	}
	raw, _ := base64.StdEncoding.DecodeString(result["backup_base64"].(string))
	if len(raw) == 0 {
		t.Fatal("base64 內容不應為空")
	}

	// 預覽
	preview, err := previewEncryptedBackup(paths, map[string]any{
		"backup_base64": result["backup_base64"], "password": "backup-pass-8",
	})
	if err != nil {
		t.Fatalf("previewEncryptedBackup: %v", err)
	}
	if preview == nil || preview["files"] == nil {
		t.Fatalf("預覽應含檔案清單: %v", preview)
	}

	// 錯誤密碼應逐字報錯
	if _, err := previewEncryptedBackup(paths, map[string]any{
		"backup_base64": result["backup_base64"], "password": "wrong-pass-9",
	}); err == nil || err.Error() != "备份密码错误或文件已损坏" {
		t.Fatalf("錯誤密碼訊息不符: %v", err)
	}

	// 還原
	restore, err := restoreEncryptedBackup(paths, map[string]any{
		"backup_base64": result["backup_base64"], "password": "backup-pass-8",
	})
	if err != nil {
		t.Fatalf("restoreEncryptedBackup: %v", err)
	}
	if restore == nil {
		t.Fatal("還原應有結果")
	}
}

// TestS3SettingsPersistence 驗證設定候選與持久化，以及 payload 不洩漏明文。
func TestS3SettingsPersistence(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	data := map[string]any{
		"enabled": true, "bucket": "bkt", "region": "ap-southeast-1",
		"use_iam_role": false, "access_key_id": "AKIDEXAMPLE",
		"secret_access_key": "shhh-secret", "backup_password": "backup-pass-8",
		"schedule": "weekly", "weekday": 2, "retention": 5,
	}
	payload, err := saveS3BackupSettings(paths, data)
	if err != nil {
		t.Fatalf("saveS3BackupSettings: %v", err)
	}
	if payload["bucket"] != "bkt" || payload["schedule"] != "weekly" || payload["weekday"] != 2 {
		t.Fatalf("payload 欄位不符: %v", payload)
	}
	if payload["access_key_configured"] != true || payload["uses_iam_role"] != false {
		t.Errorf("憑證布林不符: %v", payload)
	}
	// 持久化檢查：明文應寫入 config.json，payload 不應含任何 secret
	cfg, _ := config.Load(paths.Config)
	s3 := config.GetMap(cfg, "s3_backup")
	if config.GetStr(s3, "secret_access_key") != "shhh-secret" {
		t.Fatalf("secret 應已持久化: %v", s3)
	}
	for _, secret := range []string{"shhh-secret", "backup-pass-8", "AKIDEXAMPLE"} {
		if encoded, _ := json.Marshal(payload); strings.Contains(string(encoded), secret) {
			t.Fatalf("payload 不應洩漏明文 %s: %v", secret, payload)
		}
	}
	// 未用 IAM Role 但只填 AK 應逐字報錯（檢查位於非 IAM 分支，與上游一致；
	// 使用乾淨設定避免先前持久化的 SK 干擾）
	fresh := writeTestConfig(t, webTestConfig())
	if _, err := saveS3BackupSettings(fresh, map[string]any{
		"enabled": true, "bucket": "bkt", "use_iam_role": false, "access_key_id": "only-ak",
	}); err == nil || err.Error() != "未使用 IAM Role 时必须填写 S3 Access Key 和 Secret" {
		t.Fatalf("錯誤訊息不符: %v", err)
	}
}

// fakeS3Server 起一個最小假 S3（List OK / Put OK）供 TestConnection stub。
func fakeS3Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestS3TestConnectionOverHTTP 全鏈路：登入 → CSRF → /api/s3-backup/test。
func TestS3TestConnectionOverHTTP(t *testing.T) {
	srv := fakeS3Server(t)
	cfg := webTestConfig()
	paths := writeTestConfig(t, cfg)
	server := &PanelServer{
		paths:    paths,
		sessions: newSessionStore(),
		limiter:  newLoginLimiter(),
		web:      getWebConfig(cfg),
		job:      newJob(),
	}
	ts := httptest.NewServer(server.routes())
	defer ts.Close()

	// 登入
	body, _ := json.Marshal(map[string]any{"username": "admin", "password": "s3cret-pass"})
	resp, err := http.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&login)
	cookies := resp.Cookies()
	resp.Body.Close()

	endpoint := strings.TrimPrefix(srv.URL, "http://")
	payload, _ := json.Marshal(map[string]any{
		"bucket": "bkt", "region": "us-east-1", "endpoint_url": "http://" + endpoint,
		"use_iam_role": false, "access_key_id": "ak", "secret_access_key": "sk",
		"backup_password": "backup-pass-8",
	})
	req, _ := http.NewRequest("POST", ts.URL+"/api/s3-backup/test", bytes.NewReader(payload))
	req.Header.Set("X-CSRF-Token", login["csrf"].(string))
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if resp.StatusCode != 200 || result["ok"] != true {
		t.Fatalf("連線測試應成功: %d %v", resp.StatusCode, result)
	}
	data := result["result"].(map[string]any)
	if data["ok"] != true || data["bucket"] != "bkt" {
		t.Fatalf("測試結果不符: %v", data)
	}
}

// TestS3TestConnectionMissingPassword 未填密碼的逐字錯誤。
func TestS3TestConnectionMissingPassword(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	_, err := testS3BackupSettings(paths, map[string]any{
		"bucket": "bkt", "use_iam_role": false,
		"access_key_id": "ak", "secret_access_key": "sk",
	})
	if err == nil || err.Error() != "S3 自动备份密码至少需要 8 个字符" {
		t.Fatalf("錯誤訊息不符: %v", err)
	}
}

// TestDiscoveryScanAndImport 假 ECS 掃描 + 匯入去重（409 逐字錯誤）。
func TestDiscoveryScanAndImport(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"TotalCount":2,"Instances":{"Instance":[
			{"InstanceId":"i-1","InstanceName":"web","Status":"Running","RegionId":"cn-hangzhou"},
			{"InstanceId":"i-2","InstanceName":"db","Status":"Running"}]}}`)
	}))
	defer srv.Close()
	origRegions, origInstances := discoverRegionsFn, discoverInstancesFn
	origClient := aliyun.NewDiscoveryClient
	discoverRegionsFn = func(ak, sk string) ([]string, error) { return nil, fmt.Errorf("not used") }
	discoverInstancesFn = func(ak, sk string, regions []string, tagKey, tagValue string) (*aliyun.DiscoveryResult, error) {
		aliyun.NewDiscoveryClient = func(ak, sk, domain, version string) *aliyun.Client {
			c := aliyun.New(ak, sk, domain, version)
			c.Domain = strings.TrimPrefix(srv.URL, "http://")
			c.Scheme = "http"
			return c
		}
		return aliyun.DiscoverInstances(ak, sk, regions, tagKey, tagValue)
	}
	t.Cleanup(func() {
		discoverRegionsFn, discoverInstancesFn = origRegions, origInstances
		aliyun.NewDiscoveryClient = origClient
	})

	result, err := discoverInstances(paths, map[string]any{
		"ak": "ak", "sk": "sk", "regions": "cn-hangzhou",
	})
	if err != nil {
		t.Fatalf("discoverInstances: %v", err)
	}
	instances := result["instances"].([]any)
	if len(instances) != 2 {
		t.Fatalf("應有 2 台: %v", instances)
	}

	// 匯入
	importResult, err := importDiscoveredInstances(paths, map[string]any{
		"ak": "ak", "sk": "sk",
		"instances": []any{
			map[string]any{"region": "cn-hangzhou", "instance_id": "i-1", "name": "web"},
			map[string]any{"region": "cn-hangzhou", "instance_id": "i-2"},
		},
	})
	if err != nil {
		t.Fatalf("importDiscoveredInstances: %v", err)
	}
	if importResult["count"] != 2 {
		t.Fatalf("應匯入 2 台: %v", importResult)
	}
	cfg, _ := config.Load(paths.Config)
	if len(config.Users(cfg)) != 2 {
		t.Fatalf("users 應有 2 筆: %d", len(config.Users(cfg)))
	}
	// 重複匯入 → 409 逐字錯誤
	if _, err := importDiscoveredInstances(paths, map[string]any{
		"ak": "ak", "sk": "sk",
		"instances": []any{map[string]any{"region": "cn-hangzhou", "instance_id": "i-1"}},
	}); err == nil || err.Error() != "所选实例均已存在或数据无效" {
		t.Fatalf("錯誤訊息不符: %v", err)
	}
	// 空清單
	if _, err := importDiscoveredInstances(paths, map[string]any{
		"ak": "ak", "sk": "sk", "instances": []any{},
	}); err == nil || err.Error() != "请选择至少一个实例" {
		t.Fatalf("錯誤訊息不符: %v", err)
	}
}

// TestS3BackupPayloadNoLeak payload 與 management payload 皆不洩漏憑證明文。
func TestS3BackupPayloadNoLeak(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	cfg, _ := config.Load(paths.Config)
	cfg["s3_backup"] = map[string]any{
		"enabled": true, "bucket": "bkt", "access_key_id": "AKEXPOSED",
		"secret_access_key": "SKEXPOSED", "backup_password": "PWEXPOSED99",
	}
	_ = config.Save(paths.Config, cfg)
	payload, err := managementPayload(paths, "go")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payload)
	for _, secret := range []string{"AKEXPOSED", "SKEXPOSED", "PWEXPOSED99"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("management payload 洩漏明文 %s", secret)
		}
	}
}

// TestDefaultConfigHasS3Backup Default() 應含完整 s3_backup 欄位。
func TestDefaultConfigHasS3Backup(t *testing.T) {
	def := config.Default()
	s3 := config.GetMap(def, "s3_backup")
	if s3 == nil {
		t.Fatal("Default() 應含 s3_backup")
	}
	if _, err := s3backup.ValidateConfig(s3, nil); err != nil {
		t.Fatalf("預設 s3_backup 應通過驗證: %v", err)
	}
	if s3["retention"] != 30 || s3["time"] != "03:00" || s3["server_side_encryption"] != "AES256" {
		t.Fatalf("預設值不符: %v", s3)
	}
}

// TestRestoreWritesSafeBackup 還原前應產生 before-restore 安全備份。
func TestRestoreWritesSafeBackup(t *testing.T) {
	paths := writeTestConfig(t, webTestConfig())
	cfg, _ := config.Load(paths.Config)
	cfg["users"] = []any{map[string]any{
		"name": "HK", "ak": "a", "sk": "b", "region": "cn-hongkong", "instance_id": "i-1",
	}}
	_ = config.Save(paths.Config, cfg)
	created, err := createEncryptedBackup(paths, map[string]any{"password": "backup-pass-8"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreEncryptedBackup(paths, map[string]any{
		"backup_base64": created["backup_base64"], "password": "backup-pass-8",
	}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(paths.Config), "backups", "before-restore-*.agbackup"))
	if len(matches) != 1 {
		t.Fatalf("應有 1 份還原前安全備份: %v", matches)
	}
	_ = os.Remove(matches[0])
}
