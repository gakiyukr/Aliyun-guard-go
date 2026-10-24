// v1 備份信封的加密與解密，對應 backup_manager.py 的 encrypt_payload /
// decrypt_payload。Go 以緊湊 JSON 輸出（鍵序自動排序、不跳脫 HTML 字元），
// Python 端串流標記與整檔 JSON 兩條解密路徑皆可讀；Go 解密亦接受上游
// pretty 排版（json.Unmarshal 天然兼容）。
package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// Envelope 對應 v1 信封的八個 JSON 欄位；Iterations 以 Number 接收，
// 使非數值迭代數能命中「备份 KDF 参数无效」而非整體解析失敗（同上游語意）。
type Envelope struct {
	Format     string      `json:"format"`
	CreatedAt  string      `json:"created_at"`
	KDF        string      `json:"kdf"`
	Cipher     string      `json:"cipher"`
	Iterations json.Number `json:"iterations"`
	Salt       string      `json:"salt"`
	Nonce      string      `json:"nonce"`
	Ciphertext string      `json:"ciphertext"`
}

// nowText 對應上游 _now_text：本地時區 ISO 8601 帶時區偏移、秒精度。
func nowText() string {
	return time.Now().Format("2006-01-02T15:04:05Z07:00")
}

// aad 組裝 GCM 附加認證資料：格式字串 + NUL + 迭代數（ASCII）。
func aad(iterations int) []byte {
	return append([]byte(Format+"\x00"), []byte(strconv.Itoa(iterations))...)
}

// deriveKey 檢查迭代範圍並導出金鑰（解密路徑的迭代數來自信封，故先驗範圍）。
func deriveKey(passphrase string, salt []byte, iterations int) ([]byte, error) {
	if iterations < MinIterations || iterations > MaxIterations {
		return nil, errf("备份 KDF 参数超出安全范围")
	}
	return DeriveKey(passphrase, salt, iterations)
}

// EncryptPayload 將明文 payload（任意 JSON 相容值）加密為信封並序列化為
// 緊湊 JSON 位元組串流，明文超限時逐字報錯。
func EncryptPayload(payload any, passphrase string, iterations int) ([]byte, error) {
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(plaintext) > MaxBackupBytes {
		return nil, errf("备份内容超过 %d MiB 限制", MaxBackupBytes/1048576)
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key, err := DeriveKey(passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad(iterations))
	env := Envelope{
		Format:     Format,
		CreatedAt:  nowText(),
		KDF:        "pbkdf2-hmac-sha256",
		Cipher:     "aes-256-gcm",
		Iterations: json.Number(strconv.Itoa(iterations)),
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// decryptEnvelope 解析信封結構並執行 GCM 解密，錯誤順序與訊息逐字對應
// decrypt_payload：格式 → 迭代數 → 鹽/nonce/密文 → 大小 → 演算法 → 密碼。
func decryptEnvelope(data []byte, passphrase string) (map[string]any, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, errf("不是受支持的 Aliyun Guard 备份")
	}
	if env.Format != Format {
		return nil, errf("不是受支持的 Aliyun Guard 备份")
	}
	iterations, err := strconv.Atoi(env.Iterations.String())
	if err != nil {
		return nil, errf("备份 KDF 参数无效")
	}
	if iterations < MinIterations || iterations > MaxIterations {
		return nil, errf("备份 KDF 参数超出安全范围")
	}
	salt, err := b64decode(env.Salt)
	if err != nil {
		return nil, errf("备份中的 salt 格式无效")
	}
	nonce, err := b64decode(env.Nonce)
	if err != nil {
		return nil, errf("备份中的 nonce 格式无效")
	}
	ciphertext, err := b64decode(env.Ciphertext)
	if err != nil {
		return nil, errf("备份中的 ciphertext 格式无效")
	}
	if len(ciphertext) > MaxBackupBytes+16 {
		return nil, errf("备份内容过大")
	}
	if env.Cipher != "aes-256-gcm" {
		return nil, errf("备份加密算法不受支持")
	}
	key, err := deriveKey(passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, decErr := gcm.Open(nil, nonce, ciphertext, aad(iterations))
	if decErr != nil {
		return nil, errf("备份密码错误或文件已损坏")
	}
	var payload map[string]any
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, errf("备份解密后内容无效")
	}
	if files, ok := payload["files"].(map[string]any); !ok || files == nil {
		return nil, errf("备份内容结构无效")
	}
	return payload, nil
}

// LoadBackup 讀取備份檔並解密，對應 load_backup（Go 統一走整檔 JSON 路徑，
// 兼容上游串流與緊湊兩種排版）。
func LoadBackup(path, passphrase string) (map[string]any, error) {
	data, err := readFileCapped(path)
	if err != nil {
		return nil, errf("无法读取备份: %v", err)
	}
	return LoadBackupBytes(data, passphrase)
}

// LoadBackupBytes 解密記憶體中的備份位元組。
func LoadBackupBytes(data []byte, passphrase string) (map[string]any, error) {
	return decryptEnvelope(data, passphrase)
}

// b64decode 對應上游 _b64decode（validate=True → 標準 base64 嚴格解碼）。
func b64decode(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}

// fileEntry / fileEntries 對應 payload["files"] 的結構。
type fileEntry struct {
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
	Mode    int    `json:"mode"`
}

// filesMap 取出並型別斷言 payload["files"]（decryptEnvelope 已驗存在）。
func filesMap(payload map[string]any) map[string]fileEntry {
	raw, _ := payload["files"].(map[string]any)
	out := make(map[string]fileEntry, len(raw))
	for name, meta := range raw {
		m, _ := meta.(map[string]any)
		if m == nil {
			continue
		}
		content, _ := m["content"].(string)
		sha, _ := m["sha256"].(string)
		mode := 0
		if f, ok := m["mode"].(float64); ok {
			mode = int(f)
		}
		out[name] = fileEntry{Content: content, SHA256: sha, Mode: mode}
	}
	return out
}

// entryData 解碼單一檔案內容並驗 sha256，錯誤逐字對應 _preview_payload。
func entryData(name string, entry fileEntry) ([]byte, error) {
	data, err := b64decode(entry.Content)
	if err != nil {
		return nil, errf("备份中的 %s 格式无效", name)
	}
	if sha256.Sum256(data) != mustSum(entry.SHA256) {
		return nil, errf("备份内部校验失败: %s", name)
	}
	return data, nil
}

// mustSum 將 hex 字串轉為可比較的 32 位元組陣列（無效 hex 視為不符）。
func mustSum(hexStr string) [32]byte {
	var out [32]byte
	b, _ := hex.DecodeString(hexStr)
	if len(b) == 32 {
		copy(out[:], b)
	}
	return out
}
