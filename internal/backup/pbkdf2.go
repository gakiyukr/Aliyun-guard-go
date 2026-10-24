// 手寫 PBKDF2-HMAC-SHA256（RFC 8018），避免引入 golang.org/x/crypto 以維持
// 零第三方依賴約束。演算法與 Python hashlib.pbkdf2_hmac("sha256", ...) 逐位一致。
package backup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"unicode/utf8"
)

// pbkdf2SHA256 以鹽與迭代次數從密碼導出 dkLen 位元組金鑰。
func pbkdf2SHA256(password, salt []byte, iterations, dkLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (dkLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	block := make([]byte, hashLen)
	u := make([]byte, hashLen)
	for i := 1; i <= numBlocks; i++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(i))
		prf.Write(buf[:])
		copy(block, prf.Sum(nil))
		copy(u, block)
		for n := 2; n <= iterations; n++ {
			prf.Reset()
			prf.Write(u)
			sum := prf.Sum(u[:0])
			copy(u, sum)
			for j := range block {
				block[j] ^= u[j]
			}
		}
		dk = append(dk, block...)
	}
	return dk[:dkLen]
}

// DeriveKey 對應上游 _derive_keys：密碼不足 8 字元（以字元計，與 Python
// len(str) 語意一致）直接拒絕（逐字錯誤）。
func DeriveKey(passphrase string, salt []byte, iterations int) ([]byte, error) {
	if utf8.RuneCountInString(passphrase) < 8 {
		return nil, errf("备份密码至少需要 8 个字符")
	}
	return pbkdf2SHA256([]byte(passphrase), salt, iterations, 32), nil
}
