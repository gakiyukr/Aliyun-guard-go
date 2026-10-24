package logging

import (
	"regexp"
	"strings"
	"testing"
)

// TestLeveledLoggerLevels 驗證三個等級方法輸出正確的 LEVEL 前綴，
// 格式與上游 Formatter（asctime - LEVEL - message）一致。
func TestLeveledLoggerLevels(t *testing.T) {
	out := &strings.Builder{}
	logger := NewLeveled(out)
	logger.Printf("信息一")
	logger.Warningf("警告 %s", "二")
	logger.Errorf("错误三")
	text := out.String()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	wantLevels := []string{"INFO", "WARNING", "ERROR"}
	if len(lines) != 3 {
		t.Fatalf("應輸出 3 行，實得 %d 行: %q", len(lines), text)
	}
	for i, line := range lines {
		re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} - (\w+) - (.*)$`)
		m := re.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("第 %d 行格式不符: %q", i+1, line)
		}
		if m[1] != wantLevels[i] {
			t.Errorf("第 %d 行等級應為 %s，實得 %s", i+1, wantLevels[i], m[1])
		}
	}
	if !strings.Contains(lines[1], "警告 二") || !strings.Contains(lines[2], "错误三") {
		t.Errorf("訊息內容不符: %q", lines)
	}
}
