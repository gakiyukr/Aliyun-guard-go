package backup

// 互通人工驗證輔助（不入正式測試）：ALIYUN_GUARD_INTEROP_DIR 指向目錄時，
// 於該目錄建立備份供 Python 上游解密；ALIYUN_GUARD_INTEROP_DECRYPT 指向
// Python 產生的信封時以 LoadBackup 解密驗證。未設定環境變數即跳過。
import (
	"os"
	"testing"
)

func TestInteropManual(t *testing.T) {
	if dir := os.Getenv("ALIYUN_GUARD_INTEROP_DIR"); dir != "" {
		path, err := CreateBackup("interop-pass-1234", dir, "", true, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("created %s", path)
	}
	if env := os.Getenv("ALIYUN_GUARD_INTEROP_DECRYPT"); env != "" {
		payload, err := LoadBackup(env, "interop-pass-1234")
		if err != nil {
			t.Fatal(err)
		}
		files, _ := payload["files"].(map[string]any)
		t.Logf("decrypted %d files", len(files))
	}
}
