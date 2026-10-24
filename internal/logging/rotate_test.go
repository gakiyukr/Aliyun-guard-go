// M4 輪轉日誌測試：跨日輪轉、備份保留數、前綴格式。
package logging

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotateAndPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guard.log")
	w := New(path, 3)

	if _, err := w.Write([]byte("day-one\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "day-one") {
		t.Fatalf("現行檔應含內容: %v %s", err, data)
	}

	// 模擬跨日：手動改寫 curDate 後再寫入觸發輪轉
	w.mu.Lock()
	w.curDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	w.mu.Unlock()
	if _, err := w.Write([]byte("day-two\n")); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(path + ".*")
	if len(matches) != 1 || !strings.HasSuffix(matches[0], ".gz") {
		t.Fatalf("輪轉後應有 1 份 gzip 備份，得到 %v", matches)
	}
	data = readGzip(t, matches[0])
	if !strings.Contains(string(data), "day-one") {
		t.Fatalf("備份應含前一日內容: %s", data)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "day-two") {
		t.Fatalf("現行檔應含新內容: %s", data)
	}

	// 超出保留數的備份應被清理
	w.mu.Lock()
	today := w.curDate
	w.mu.Unlock()
	for i := 5; i >= 1; i-- {
		old := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if err := os.WriteFile(path+"."+old, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.mu.Lock()
	w.curDate = ""
	_ = w.rotateLocked(today) // 觸發清理
	w.mu.Unlock()
	matches, _ = filepath.Glob(path + ".*")
	if len(matches) > 3 {
		t.Fatalf("備份數不應超過保留數 3，得到 %v", matches)
	}
	_ = w.Close()
}

// readGzip 讀取 gzip 壓縮的備份檔並回傳解壓內容。
func readGzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPrefixWriterFormat(t *testing.T) {
	var buf strings.Builder
	w := &PrefixWriter{Next: writerFunc(func(p []byte) (int, error) { return buf.WriteString(string(p)) }), Level: "INFO"}
	if _, err := w.Write([]byte("訊息\n")); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	if !strings.HasPrefix(line, time.Now().Format("2006-01-02")) ||
		!strings.Contains(line, " - INFO - 訊息\n") {
		t.Fatalf("前綴格式不符: %q", line)
	}
	// 空行不輸出
	n := buf.Len()
	_, _ = w.Write([]byte("\n"))
	if buf.Len() != n {
		t.Fatal("空行不應輸出")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
