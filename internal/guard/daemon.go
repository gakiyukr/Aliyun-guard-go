// 守護行程主迴圈，對應 Python 版 run_daemon / run_scheduled。
// 節奏與 Python 版一致：每次睡眠取「距下次完整檢測」與「距下一分鐘邊界」較小值，
// 確保排程邊界（HH:MM）每分鐘都會被檢查。
package guard

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/state"
)

// heartbeatPath 回傳 heartbeat.json 路徑（與 config.json 同目錄）。
func heartbeatPath(paths config.Paths) string {
	return filepath.Join(filepath.Dir(paths.Config), "heartbeat.json")
}

// disabled 確認 disabled 旗標檔是否存在（對應 run_scheduled 的 (APP_DIR/"disabled") 檢查）。
func disabled(paths config.Paths) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(paths.Config), "disabled"))
	return err == nil
}

// RunDaemon 進入守護迴圈，直到收到 SIGINT/SIGTERM。
func RunDaemon(paths config.Paths) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	stop := make(chan struct{})
	go func() {
		<-sigCh
		close(stop)
	}()

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return err
	}
	if config.GetBool(cfg, "notify_on_daemon_start", false) {
		if err := notifyDaemonStart(cfg); err != nil {
			Logger.Errorf("[Telegram] 启动通知发送失败: %v", err)
		}
	}
	Logger.Printf("保活服务已启动（检测间隔 %d 秒）", config.GetInt(cfg, "interval_seconds", 300))

	for {
		if err := state.WriteHeartbeat(heartbeatPath(paths), "running", "守护循环运行中"); err != nil {
			Logger.Warningf("心跳写入失败: %v", err)
		}
		// 每輪重新載入設定，支援熱修改 config.json
		cfg, err = config.Load(paths.Config)
		if err != nil {
			Logger.Warningf("config.json 载入失败，沿用上一份設定: %v", err)
		}
		st, err := state.Load(paths.State)
		if err != nil {
			Logger.Warningf("state.json 载入失败，視為空狀態: %v", err)
			st = map[string]any{}
		}

		now := time.Now()
		due := !disabled(paths) &&
			(IsDue(cfg, st, now) || HasDueSchedule(cfg, st, now) || MonthlyResetDue(st, now))
		if due {
			if release, ok := acquireCycleLock(paths); ok {
				RunCycle(paths, cfg, st, false, false)
				release()
			} else {
				Logger.Warningf("已有检测正在运行，本次计划任务跳过")
			}
		}

		// S3 備份到期檢查（對應 run_s3_backup；停用/未到期時靜默）
		s3BackupCheck(cfg, paths, now)

		wait := SchedulerWait(cfg, st)
		Logger.Printf("下一次调度等待 %s", wait.Truncate(time.Second))
		select {
		case <-stop:
			log.Printf("收到退出信号，保活服务已停止")
			return nil
		case <-time.After(wait):
		}
	}
}

// notifyDaemonStart 守護行程啟動通知，對應 notify_on_daemon_start 分支。
func notifyDaemonStart(cfg map[string]any) error {
	if cfg == nil {
		return errors.New("設定未載入")
	}
	return notify.SendMessage(cfg, fmt.Sprintf("阿里云保活服务已启动\n时间: %s", time.Now().Format("2006-01-02 15:04:05")))
}
