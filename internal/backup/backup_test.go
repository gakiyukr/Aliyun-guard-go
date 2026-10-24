package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// encryptRaw 以既有金鑰/nonce 做 GCM 加密（供測試組裝 Python 式信封）。
func encryptRaw(plaintext, key, nonce []byte, iterations int) ([]byte, error) {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	return gcm.Seal(nil, nonce, plaintext, aad(iterations)), nil
}

// TestPBKDF2SHA256 採用 RFC 7914 / 公開 PBKDF2-HMAC-SHA256 測試向量，
// 驗證手寫實作與 Python hashlib.pbkdf2_hmac 逐位一致。
func TestPBKDF2SHA256(t *testing.T) {
	cases := []struct {
		pass, salt string
		iter       int
		want       string
	}{
		{"password", "salt", 1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"password", "salt", 4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		got := hex.EncodeToString(pbkdf2SHA256([]byte(c.pass), []byte(c.salt), c.iter, 32))
		if got != c.want {
			t.Errorf("PBKDF2(c=%d) = %s, 應為 %s", c.iter, got, c.want)
		}
	}
	if _, err := DeriveKey("短密碼", make([]byte, 16), 1000); err == nil || err.Error() != "备份密码至少需要 8 个字符" {
		t.Errorf("短密碼錯誤訊息不符: %v", err)
	}
}

// TestEnvelopeRoundTrip 驗證加密→解密往返與信封欄位。
func TestEnvelopeRoundTrip(t *testing.T) {
	payload := map[string]any{
		"format":     Format,
		"created_at": "2026-09-10T00:00:00+08:00",
		"source":     "Aliyun Guard",
		"files": map[string]any{
			"config.json": map[string]any{"content": "e30=", "sha256": "abc", "mode": 384},
		},
	}
	data, err := EncryptPayload(payload, "long-password-1", PBKDF2Iterations)
	if err != nil {
		t.Fatalf("加密失敗: %v", err)
	}
	// 緊湊 JSON、無 HTML 跳脫
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("信封非合法 JSON: %v", err)
	}
	if !strings.Contains(string(data), `"format":"`+Format+`"`) {
		t.Errorf("信封應為緊湊 JSON: %.120s", data)
	}
	decoded, err := LoadBackupBytes(data, "long-password-1")
	if err != nil {
		t.Fatalf("解密失敗: %v", err)
	}
	if _, ok := decoded["files"]; !ok {
		t.Errorf("payload 應含 files")
	}
	// 錯誤密碼
	if _, err := LoadBackupBytes(data, "wrong-password"); err == nil || err.Error() != "备份密码错误或文件已损坏" {
		t.Errorf("錯誤密碼訊息不符: %v", err)
	}
	// 篡改密文
	var env map[string]json.RawMessage
	_ = json.Unmarshal(data, &env)
	tampered := strings.Replace(string(data), `"ciphertext":"`, `"ciphertext":"AAAA`, 1)
	if _, err := LoadBackupBytes([]byte(tampered), "long-password-1"); err == nil || err.Error() != "备份密码错误或文件已损坏" {
		t.Errorf("篡改後訊息不符: %v", err)
	}
	// 格式不符
	bad, _ := json.Marshal(map[string]any{"format": "other"})
	if _, err := LoadBackupBytes(bad, "long-password-1"); err == nil || err.Error() != "不是受支持的 Aliyun Guard 备份" {
		t.Errorf("格式錯誤訊息不符: %v", err)
	}
}

// TestLoadPrettyEnvelope 驗證可解上游 Python pretty 排版信封（多行 JSON、
// 帶空格的 "ciphertext": " 標記）。
func TestLoadPrettyEnvelope(t *testing.T) {
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	inner := map[string]any{"format": Format, "files": map[string]any{}}
	plaintext, _ := json.Marshal(inner)
	key, _ := DeriveKey("long-password-1", salt, MinIterations)
	env, err := encryptRaw(plaintext, key, nonce, MinIterations)
	if err != nil {
		t.Fatalf("加密失敗: %v", err)
	}
	// 以 Python _write_encrypted_payload 的排版手組 pretty 信封
	pretty := "{\n" +
		`  "format": "` + Format + `",` + "\n" +
		`  "created_at": "2026-09-10T00:00:00+08:00",` + "\n" +
		`  "kdf": "pbkdf2-hmac-sha256",` + "\n" +
		`  "cipher": "aes-256-gcm",` + "\n" +
		`  "iterations": ` + strconv.Itoa(MinIterations) + `,` + "\n" +
		`  "salt": "` + base64.StdEncoding.EncodeToString(salt) + `",` + "\n" +
		`  "nonce": "` + base64.StdEncoding.EncodeToString(nonce) + `",` + "\n" +
		`  "ciphertext": "` + base64.StdEncoding.EncodeToString(env) + "\"\n}\n"
	payload, err := LoadBackupBytes([]byte(pretty), "long-password-1")
	if err != nil {
		t.Fatalf("應可解 pretty 信封: %v", err)
	}
	if _, ok := payload["files"]; !ok {
		t.Errorf("payload 應含 files")
	}
}

// TestSafeRelativePath 白名單逐項驗證。
func TestSafeRelativePath(t *testing.T) {
	allowed := []string{"config.json", "state.json", "telegram-control-state.json",
		"s3-backup-state.json", "service_backend", "logs/guard.log", "logs/instances/x.log"}
	for _, name := range allowed {
		if _, err := SafeRelativePath(name); err != nil {
			t.Errorf("%s 應允許: %v", name, err)
		}
	}
	denied := []struct {
		name string
		want string
	}{
		{"", "备份包含不安全路径"},
		{"../escape", "备份包含不安全路径"},
		{"/abs/path", "备份包含不允许恢复的文件: abs/path"}, // 上游 strip("/") 後落入白名單判斷
		{".hidden", "备份包含不安全路径"},
		{"aliyun_guard.py", "备份包含不允许恢复的文件: aliyun_guard.py"},
		{"logs/../config.json", "备份包含不安全路径"},
	}
	for _, c := range denied {
		_, err := SafeRelativePath(c.name)
		if err == nil || err.Error() != c.want {
			t.Errorf("%q 錯誤不符: got %v, want %s", c.name, err, c.want)
		}
	}
}

// TestCreateAndRestore 全流程：建立 → 預覽 → 還原（含安全備份存在性）。
func TestCreateAndRestore(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, "config.json"), `{"users":[{"name":"a"}]}`)
	writeFile(t, filepath.Join(appDir, "state.json"), `{}`)
	writeFile(t, filepath.Join(appDir, "logs", "guard.log"), "log line\n")

	backupPath, err := CreateBackup("long-password-1", appDir, "", true, true)
	if err != nil {
		t.Fatalf("建立備份失敗: %v", err)
	}
	preview, err := PreviewRestore(backupPath, "long-password-1", appDir)
	if err != nil {
		t.Fatalf("預覽失敗: %v", err)
	}
	if preview.Summary["instances"] != 1 {
		t.Errorf("摘要 instances 應為 1: %v", preview.Summary)
	}
	// 修改 config 後還原應標記 replace
	writeFile(t, filepath.Join(appDir, "config.json"), `{"users":[]}`)
	preview2, err := PreviewRestore(backupPath, "long-password-1", appDir)
	if err != nil {
		t.Fatalf("預覽失敗: %v", err)
	}
	found := false
	for _, f := range preview2.Files {
		if f.Path == "config.json" {
			found = f.Action == "replace"
		}
	}
	if !found {
		t.Errorf("config.json 應為 replace")
	}
	// 還原
	result, err := RestoreBackup(backupPath, "long-password-1", appDir, false)
	if err != nil {
		t.Fatalf("還原失敗: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(appDir, "config.json"))
	if string(data) != `{"users":[{"name":"a"}]}` {
		t.Errorf("config.json 未還原: %s", data)
	}
	if _, err := os.Stat(result["safety_backup"].(string)); err != nil {
		t.Errorf("安全備份不存在: %v", err)
	}
	// include_logs=false 時不覆寫日誌（此處 logs 不在備份外被動過，僅驗 restored 不含 logs）
	for _, name := range result["restored"].([]string) {
		if strings.HasPrefix(name, "logs/") {
			t.Errorf("include_logs=false 不應還原 logs: %s", name)
		}
	}
	// 缺 config.json 的備份不可還原
	appDir2 := t.TempDir()
	writeFile(t, filepath.Join(appDir2, "state.json"), `{}`)
	if _, err := CreateBackup("long-password-1", appDir2, "", true, false); err == nil ||
		err.Error() != "未找到 config.json，无法创建完整备份" {
		t.Errorf("缺 config.json 錯誤不符: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
