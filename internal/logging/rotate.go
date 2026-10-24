// 每日輪轉日誌寫入器，對應 Python 版 TimedRotatingFileHandler(when="midnight",
// backupCount=14)：跨日時將現行檔案更名為 base.<當日日期>（該日誌的覆蓋日），
// 備份 gzip 壓縮（對應上游 spot_ddns 的 gzip_rotator），並清理超過保留數的備份。
// 目錄 0700、檔案 0600，與上游權限一致。
package logging

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Writer 為 append 模式的每日輪轉檔案寫入器，執行緒安全。
type Writer struct {
	mu      sync.Mutex
	path    string
	keep    int
	file    *os.File
	curDate string
}

// New 建立輪轉寫入器（不立即開檔，首次寫入時開啟）。
func New(path string, keep int) *Writer {
	return &Writer{path: path, keep: keep}
}

// Write 實作 io.Writer；跨日時先輪轉。log.Logger 需設 flags 0，
// 時間戳與等級前綴由 PrefixWriter 統一加入。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if err := w.rotateLocked(today); err != nil {
		return 0, err
	}
	if w.file == nil {
		if err := w.openLocked(); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// rotateLocked 跨日輪轉：更名現行檔為前一日日期，清理過期備份。
func (w *Writer) rotateLocked(today string) error {
	if w.curDate == today {
		return nil
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if w.curDate != "" {
		if _, err := os.Stat(w.path); err == nil {
			rotated := w.path + "." + w.curDate
			_ = os.Rename(w.path, rotated)
			// 輪轉備份 gzip 壓縮（對應上游 gzip_rotator/namer）：壓縮失敗時
			// 保留未壓縮備份，仍納入 prune 的保留計數，不影響正確性。
			_ = gzipFile(rotated)
		}
	}
	w.curDate = today
	w.pruneLocked()
	return nil
}

func (w *Writer) openLocked() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(w.path), 0o700)
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_ = os.Chmod(w.path, 0o600)
	w.file = f
	return nil
}

// pruneLocked 依備份日期排序，僅保留最近 keep 份。
func (w *Writer) pruneLocked() {
	dir := filepath.Dir(w.path)
	base := filepath.Base(w.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// 同時匹配未壓縮備份（base.DATE，含升級前遺留）與 gzip 備份（base.DATE.gz），
	// 兩者合併計入 keep 保留數。
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.(\d{4}-\d{2}-\d{2})(\.gz)?$`)
	type dated struct {
		name string
		date string
	}
	var backups []dated
	for _, entry := range entries {
		if m := pattern.FindStringSubmatch(entry.Name()); m != nil {
			backups = append(backups, dated{entry.Name(), m[1]})
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].date > backups[j].date })
	for i, b := range backups {
		if i >= w.keep {
			_ = os.Remove(filepath.Join(dir, b.name))
		}
	}
}

// gzipFile 將 src 壓縮為 src+".gz" 後刪除原檔（先寫 .tmp 再改名的兩段式，
// 避免中途失敗留下半截 .gz 被誤當有效備份）。
func gzipFile(src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	// 注意須在刪除 src 之前明確關閉（不能只靠 defer，那會拖到函式返回）。
	closeIn := func() { _ = in.Close() }
	defer closeIn()
	tmp := src + ".gz.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		gz.Close()
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := gz.Close(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, src+".gz"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	closeIn()
	// Windows 上剛關閉的檔案可能被防毒掃描短暫鎖住，Remove 需重試較長窗口；
	// 徹底失敗時保留未壓縮備份（prune 仍會納入保留計數，不影響正確性）。
	for i := 0; i < 5; i++ {
		if err := os.Remove(src); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// Close 關閉現行檔案。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

// PrefixWriter 為每行加入「2006-01-02 15:04:05 - LEVEL - 」前綴的裝飾器，
// 對應上游 logging.Formatter("%(asctime)s - %(levelname)s - %(message)s")。
type PrefixWriter struct {
	Next  io.Writer
	Level string
}

func (p *PrefixWriter) Write(data []byte) (int, error) {
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return len(data), nil
	}
	line := fmt.Sprintf("%s - %s - %s\n", time.Now().Format("2006-01-02 15:04:05"), p.Level, text)
	if _, err := p.Next.Write([]byte(line)); err != nil {
		return 0, err
	}
	return len(data), nil
}
