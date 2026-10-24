// M5 面板操作：S3 備份、加密備份/還原、實例探索。對應 web_actions.py 的
// save_s3_backup_settings / test_s3_backup_settings / run_s3_backup_now /
// list_s3_backups / preview_s3_backup / restore_s3_backup /
// create_encrypted_backup / preview_encrypted_backup /
// restore_encrypted_backup / discover_instances / import_discovered_instances。
package web

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/backup"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/s3backup"
)

// appDir 對應上游 DATA_DIR（config.json 所在目錄）。
func appDir(paths config.Paths) string {
	return filepath.Dir(paths.Config)
}

// --- S3 備份 payload（對應 s3_backup_payload）---

// s3BackupPayload 組出面板 S3 區塊資料：設定欄位 + 狀態檔摘要，
// 憑證僅以布林呈現，絕不回傳明文。
func s3BackupPayload(value map[string]any, paths config.Paths) map[string]any {
	cfg, err := s3backup.NormalizedConfig(value)
	if err != nil {
		// 正規化失敗時退回停用預設（與上游啟用前行為一致）
		cfg = &s3backup.Config{}
	}
	status := s3backup.ReadStatus(appDir(paths))
	return map[string]any{
		"enabled":                    cfg.Enabled,
		"bucket":                     cfg.Bucket,
		"region":                     cfg.Region,
		"endpoint_url":               cfg.EndpointURL,
		"prefix":                     cfg.Prefix,
		"addressing_style":           cfg.AddressingStyle,
		"access_key_configured":      cfg.AccessKeyID != "",
		"uses_iam_role":              cfg.AccessKeyID == "",
		"session_token_configured":   cfg.SessionToken != "",
		"backup_password_configured": cfg.BackupPassword != "",
		"schedule":                   cfg.Schedule,
		"time":                       cfg.Time,
		"weekday":                    cfg.Weekday,
		"retention":                  cfg.Retention,
		"include_state":              cfg.IncludeState,
		"include_logs":               cfg.IncludeLogs,
		"notification_mode":          cfg.NotificationMode,
		"server_side_encryption":     cfg.ServerSideEncryption,
		"kms_key_configured":         cfg.KMSKeyID != "",
		"status": map[string]any{
			"last_attempt_at": orNil(status["last_attempt_at"]),
			"last_success_at": orNil(status["last_success_at"]),
			"last_key":        orNil(status["last_key"]),
			"last_error":      orNil(status["last_error"]),
		},
	}
}

// orNil 將空字串轉 nil（與上游狀態欄位 None 語意一致）。
func orNil(v any) any {
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}

// --- S3 設定候選（對應 _s3_candidate）---

// s3CandidateFields 為面板可直接更新的字串設定欄位。
var s3CandidateFields = []string{
	"bucket", "region", "endpoint_url", "prefix", "schedule", "time",
	"notification_mode", "server_side_encryption", "addressing_style",
}

// s3Candidate 依面板輸入組出完整 S3 設定候選（欄位僅在提供時更新）。
// save=true 表示將持久化（驗證僅在 enabled 時要求 ready 欄位）。
func s3Candidate(paths config.Paths, data map[string]any, save bool) (map[string]any, error) {
	if data == nil {
		return nil, &PanelError{"S3 配置必须是对象", 400}
	}
	cfg := mustLoad(paths)
	current := orMap(cfg["s3_backup"])
	candidate := config.DefaultS3Backup()
	for key, value := range current {
		candidate[key] = value
	}
	// 字串欄位：提供時 trim 覆寫
	for _, field := range s3CandidateFields {
		if raw, ok := data[field]; ok {
			candidate[field] = strings.TrimSpace(toString(raw))
		}
	}
	// 布林欄位
	for _, field := range []string{"enabled", "include_state", "include_logs"} {
		currentBool, _ := candidate[field].(bool)
		v, err := asBool(data, field, currentBool)
		if err != nil {
			return nil, err
		}
		candidate[field] = v
	}
	weekday, _ := candidate["weekday"].(int)
	if f, ok := candidate["weekday"].(float64); ok {
		weekday = int(f)
	}
	retention, _ := candidate["retention"].(int)
	if f, ok := candidate["retention"].(float64); ok {
		retention = int(f)
	}
	weekdayValue, err := asInt(data, "weekday", weekday, 0, 6)
	if err != nil {
		return nil, err
	}
	candidate["weekday"] = weekdayValue
	retentionValue, err := asInt(data, "retention", retention, 1, 365)
	if err != nil {
		return nil, err
	}
	candidate["retention"] = retentionValue

	enabled := candidate["enabled"] == true
	useIAMRole, err := asBool(data, "use_iam_role", toString(candidate["access_key_id"]) == "")
	if err != nil {
		return nil, err
	}
	if useIAMRole {
		candidate["access_key_id"] = ""
		candidate["secret_access_key"] = ""
		candidate["session_token"] = ""
	} else {
		for _, field := range []string{"access_key_id", "secret_access_key", "session_token"} {
			if entered := strings.TrimSpace(toString(data[field])); entered != "" {
				candidate[field] = entered
			}
		}
		clearToken, _ := data["clear_session_token"].(bool)
		if clearToken {
			candidate["session_token"] = ""
		}
		ak := toString(candidate["access_key_id"])
		sk := toString(candidate["secret_access_key"])
		if (enabled || !save) && (ak == "" || sk == "") {
			return nil, &PanelError{"未使用 IAM Role 时必须填写 S3 Access Key 和 Secret", 400}
		}
	}
	if entered := strings.TrimSpace(toString(data["backup_password"])); entered != "" {
		candidate["backup_password"] = entered
	}
	if entered := strings.TrimSpace(toString(data["kms_key_id"])); entered != "" {
		candidate["kms_key_id"] = entered
	} else if toString(candidate["server_side_encryption"]) != "aws:kms" {
		candidate["kms_key_id"] = ""
	}
	ready := enabled || !save
	if _, err := s3backup.ValidateConfig(candidate, &ready); err != nil {
		return nil, &PanelError{err.Error(), 400}
	}
	return candidate, nil
}

// s3Secrets 集合供錯誤脫敏。
func s3Secrets(candidate map[string]any) []string {
	return []string{
		toString(candidate["access_key_id"]), toString(candidate["secret_access_key"]),
		toString(candidate["session_token"]), toString(candidate["backup_password"]),
	}
}

// saveS3BackupSettings 驗證並持久化 S3 設定，回傳面板 payload。
func saveS3BackupSettings(paths config.Paths, data map[string]any) (map[string]any, error) {
	candidate, err := s3Candidate(paths, data, true)
	if err != nil {
		return nil, err
	}
	cfg := mustLoad(paths)
	cfg["s3_backup"] = candidate
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return s3BackupPayload(candidate, paths), nil
}

// testS3BackupSettings 以候選設定執行連線測試（不持久化）。
func testS3BackupSettings(paths config.Paths, data map[string]any) (map[string]any, error) {
	candidate, err := s3Candidate(paths, data, false)
	if err != nil {
		return nil, err
	}
	result, err := s3backup.TestConnection(candidate)
	if err != nil {
		return nil, &PanelError{aliyun.CompactError(err, s3Secrets(candidate)), 502}
	}
	return result, nil
}

// runS3BackupNow 立即建立並上傳 S3 備份（對應 run_s3_backup_now）。
func runS3BackupNow(paths config.Paths) (map[string]any, error) {
	cfg := mustLoad(paths)
	s3cfg, err := s3backup.NormalizedConfig(orMap(cfg["s3_backup"]))
	if err != nil {
		return nil, &PanelError{err.Error(), 502}
	}
	result, err := s3backup.CreateAndUpload(s3cfg, appDir(paths), time.Now())
	if err != nil {
		secrets := []string{s3cfg.AccessKeyID, s3cfg.SecretAccessKey, s3cfg.SessionToken, s3cfg.BackupPassword}
		return nil, &PanelError{aliyun.CompactError(err, secrets), 502}
	}
	return result, nil
}

// listS3Backups 列出遠端 S3 備份（上限 100，對應 list_s3_backups）。
func listS3Backups(paths config.Paths) (map[string]any, error) {
	cfg := mustLoad(paths)
	s3cfg, err := s3backup.NormalizedConfig(orMap(cfg["s3_backup"]))
	if err != nil {
		return nil, &PanelError{err.Error(), 502}
	}
	items, err := s3backup.ListBackups(s3cfg, nil, 100)
	if err != nil {
		secrets := []string{s3cfg.AccessKeyID, s3cfg.SecretAccessKey, s3cfg.SessionToken}
		return nil, &PanelError{aliyun.CompactError(err, secrets), 502}
	}
	return map[string]any{"items": toAnySliceItems(items), "count": len(items)}, nil
}

// toAnySliceItems 轉 []any 供 JSON 序列化。
func toAnySliceItems(items []s3backup.BackupItem) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

// downloadForRestore 下載遠端備份並回傳本機路徑（呼叫端負責刪除）。
func downloadForRestore(paths config.Paths, key string) (string, map[string]any, error) {
	cfg := mustLoad(paths)
	s3cfg, err := s3backup.NormalizedConfig(orMap(cfg["s3_backup"]))
	if err != nil {
		return "", nil, &PanelError{err.Error(), 502}
	}
	path, err := s3backup.DownloadBackup(orMap(cfg["s3_backup"]), key, appDir(paths))
	if err != nil {
		secrets := []string{s3cfg.AccessKeyID, s3cfg.SecretAccessKey, s3cfg.SessionToken, s3cfg.BackupPassword}
		return "", nil, &PanelError{aliyun.CompactError(err, secrets), 502}
	}
	return path, orMap(cfg["s3_backup"]), nil
}

// previewS3Backup 下載遠端備份後產出還原預覽（檔案用後即刪）。
func previewS3Backup(paths config.Paths, key string) (map[string]any, error) {
	path, s3map, err := downloadForRestore(paths, key)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	preview, err := backup.PreviewRestore(path, toString(s3map["backup_password"]), appDir(paths))
	if err != nil {
		return nil, &PanelError{err.Error(), 400}
	}
	return previewToMap(preview), nil
}

// restoreS3Backup 下載遠端備份並還原（檔案用後即刪）。
func restoreS3Backup(paths config.Paths, key string, includeLogs bool) (map[string]any, error) {
	path, s3map, err := downloadForRestore(paths, key)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	return backup.RestoreBackup(path, toString(s3map["backup_password"]), appDir(paths), includeLogs)
}

// --- 加密備份（對應 create/preview/restore_encrypted_backup）---

// decodeBackupData 解出上傳的備份內容並寫入暫存檔（0600），回傳路徑。
func decodeBackupData(paths config.Paths, data map[string]any) (string, error) {
	encoded := strings.TrimSpace(toString(data["backup_base64"]))
	if encoded == "" {
		return "", &PanelError{"请选择备份文件", 400}
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", &PanelError{"备份文件编码无效", 400}
	}
	if len(content) > backup.MaxBackupFileBytes {
		return "", &PanelError{"备份文件过大", 413}
	}
	directory := filepath.Join(appDir(paths), "backups")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	temporary := filepath.Join(directory,
		fmt.Sprintf(".web-upload-%d-%d.agbackup", os.Getpid(), time.Now().UnixMilli()))
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(temporary, 0o600)
	return temporary, nil
}

// createEncryptedBackup 建立加密備份並以 base64 回傳（對應 create_encrypted_backup）。
func createEncryptedBackup(paths config.Paths, data map[string]any) (map[string]any, error) {
	password := toString(data["password"])
	includeState, err := asBool(data, "include_state", true)
	if err != nil {
		return nil, err
	}
	includeLogs, err := asBool(data, "include_logs", true)
	if err != nil {
		return nil, err
	}
	path, err := backup.CreateBackup(password, appDir(paths), "", includeState, includeLogs)
	if err != nil {
		return nil, &PanelError{err.Error(), 400}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, &PanelError{err.Error(), 500}
	}
	return map[string]any{
		"filename":      filepath.Base(path),
		"backup_base64": base64.StdEncoding.EncodeToString(content),
		"size":          len(content),
	}, nil
}

// previewEncryptedBackup 預覽上傳備份的還原內容（暫存檔用後即刪）。
func previewEncryptedBackup(paths config.Paths, data map[string]any) (map[string]any, error) {
	temporary, err := decodeBackupData(paths, data)
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary)
	preview, err := backup.PreviewRestore(temporary, toString(data["password"]), appDir(paths))
	if err != nil {
		return nil, &PanelError{err.Error(), 400}
	}
	return previewToMap(preview), nil
}

// restoreEncryptedBackup 還原上傳的備份（暫存檔用後即刪）。
func restoreEncryptedBackup(paths config.Paths, data map[string]any) (map[string]any, error) {
	includeLogs, err := asBool(data, "include_logs", true)
	if err != nil {
		return nil, err
	}
	temporary, err := decodeBackupData(paths, data)
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary)
	return backup.RestoreBackup(temporary, toString(data["password"]), appDir(paths), includeLogs)
}

// previewToMap 將 RestorePreview 轉 JSON map。
func previewToMap(preview *backup.RestorePreview) map[string]any {
	if preview == nil {
		return nil
	}
	files := make([]any, 0, len(preview.Files))
	for _, f := range preview.Files {
		files = append(files, f)
	}
	return map[string]any{"files": files, "summary": preview.Summary, "created_at": preview.CreatedAt}
}

// --- 實例探索（對應 discover_instances / import_discovered_instances）---

// regionSplitRe 將 "a,b;c" 形式的字串拆成 region 清單。
var regionSplitRe = regexp.MustCompile(`[;,]`)

// discoverRegionsFn / discoverInstancesFn 為套件級函式變數：測試中替換為
// 指向 httptest 假 ECS 的 stub（aliyun 套件的工廠變數不對外公開）。
var (
	discoverRegionsFn   = aliyun.DiscoverRegions
	discoverInstancesFn = aliyun.DiscoverInstances
)

// discoverInstances 掃描 ECS 實例並標記已設定項（對應 discover_instances）。
func discoverInstances(paths config.Paths, data map[string]any) (map[string]any, error) {
	if data == nil {
		return nil, &PanelError{"实例发现参数无效", 400}
	}
	ak, err := requiredText(data, "ak", "", "AccessKey ID")
	if err != nil {
		return nil, err
	}
	sk, err := requiredText(data, "sk", "", "AccessKey Secret")
	if err != nil {
		return nil, err
	}
	var regions []string
	switch raw := data["regions"].(type) {
	case nil:
	case string:
		for _, part := range regionSplitRe.Split(raw, -1) {
			if part = strings.TrimSpace(part); part != "" {
				regions = append(regions, part)
			}
		}
	case []any:
		for _, item := range raw {
			if part := strings.TrimSpace(toString(item)); part != "" {
				regions = append(regions, part)
			}
		}
	default:
		return nil, &PanelError{"Region 列表格式无效", 400}
	}
	if len(regions) == 0 {
		list, err := discoverRegionsFn(ak, sk)
		if err != nil {
			return nil, &PanelError{fmt.Sprintf("自动获取 Region 失败: %s", aliyun.CompactError(err, []string{ak, sk})), 502}
		}
		regions = list
	}
	result, err := discoverInstancesFn(ak, sk, regions,
		strings.TrimSpace(toString(data["tag_key"])), strings.TrimSpace(toString(data["tag_value"])))
	if err != nil {
		return nil, &PanelError{aliyun.CompactError(err, []string{ak, sk}), 502}
	}
	cfg := mustLoad(paths)
	type identity struct{ ak, region, id string }
	existing := map[identity]bool{}
	for _, user := range config.Users(cfg) {
		existing[identity{toString(user["ak"]), toString(user["region"]), toString(user["instance_id"])}] = true
	}
	instances := make([]any, 0, len(result.Instances))
	for _, item := range result.Instances {
		entry := map[string]any{
			"region": item.Region, "instance_id": item.InstanceID, "name": item.Name,
			"status": item.Status, "zone_id": item.ZoneID, "instance_type": item.InstanceType,
			"public_ip": item.PublicIP, "tags": item.Tags,
			"already_configured": existing[identity{ak, item.Region, item.InstanceID}],
		}
		instances = append(instances, entry)
	}
	return map[string]any{
		"instances": instances,
		"errors":    result.Errors,
		"regions":   result.Regions,
	}, nil
}

// importDiscoveredInstances 匯入勾選的實例（去重；對應 import_discovered_instances）。
func importDiscoveredInstances(paths config.Paths, data map[string]any) (map[string]any, error) {
	ak, err := requiredText(data, "ak", "", "AccessKey ID")
	if err != nil {
		return nil, err
	}
	sk, err := requiredText(data, "sk", "", "AccessKey Secret")
	if err != nil {
		return nil, err
	}
	selected, _ := data["instances"].([]any)
	if len(selected) == 0 {
		return nil, &PanelError{"请选择至少一个实例", 400}
	}
	if len(selected) > 100 {
		return nil, &PanelError{"一次最多导入 100 个实例", 400}
	}
	limit, err := asNumber(data, "traffic_limit_gb", 180, 0.01)
	if err != nil {
		return nil, err
	}
	actionsEnabled, err := asBool(data, "actions_enabled", true)
	if err != nil {
		return nil, err
	}
	billingSite := toString(orDefaultAny(data["billing_site"], "china"))
	if billingSite == "" {
		billingSite = "china"
	}
	billingRaw, ok := billingPresets[billingSite]
	if !ok {
		billingRaw = billingPresets["china"]
	}
	billing := copyConfigMap(billingRaw)
	cfg := mustLoad(paths)
	users := config.Users(cfg)
	identities := map[string]bool{}
	for _, user := range users {
		identities[toString(user["ak"])+"|"+toString(user["region"])+"|"+toString(user["instance_id"])] = true
	}
	imported := []string{}
	skipped := []string{}
	for _, raw := range selected {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		region := strings.TrimSpace(toString(entry["region"]))
		instanceID := strings.TrimSpace(toString(entry["instance_id"]))
		key := ak + "|" + region + "|" + instanceID
		if region == "" || instanceID == "" || identities[key] {
			name := instanceID
			if name == "" {
				name = "无效实例"
			}
			skipped = append(skipped, name)
			continue
		}
		name := strings.TrimSpace(toString(orDefaultAny(entry["name"], instanceID)))
		if len([]rune(name)) > 80 {
			name = string([]rune(name)[:80])
		}
		user := map[string]any{
			"name": name, "ak": ak, "sk": sk,
			"region": region, "instance_id": instanceID,
			"traffic_limit_gb":     limit,
			"actions_enabled":      actionsEnabled,
			"instance_log_enabled": false,
			"paused":               false,
			"billing":              billing,
			"schedule":             copyConfigMap(config.DefaultSchedule()),
		}
		users = append(users, user)
		identities[key] = true
		imported = append(imported, instanceID)
	}
	if len(imported) == 0 {
		return nil, &PanelError{"所选实例均已存在或数据无效", 409}
	}
	cfg["users"] = toAnySlice(users)
	if err := saveConfig(paths, cfg); err != nil {
		return nil, err
	}
	return map[string]any{"imported": imported, "skipped": skipped, "count": len(imported)}, nil
}

// copyConfigMap 淺拷貝 config map（對應 _copy）。
func copyConfigMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
