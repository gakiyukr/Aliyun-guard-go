// aliyun-guard-go 守護工具進入點。
// 子命令與 Python 版 CLI 對應：run（守護）、once（執行一輪）、dry-run（演練）、
// status（狀態）、serve（網路面板）、bot（Telegram 控制）、test-telegram、version。
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	// 內嵌 IANA 時區資料庫：排程時區（schedule.timezone，如 Asia/Taipei）在
	// 未安裝 zoneinfo 的精簡系統上也能解析，不依賴主機環境。
	_ "time/tzdata"

	"aliyun-guard-go/internal/aliyun"
	"aliyun-guard-go/internal/config"
	"aliyun-guard-go/internal/guard"
	"aliyun-guard-go/internal/logging"
	"aliyun-guard-go/internal/notify"
	"aliyun-guard-go/internal/state"
	"aliyun-guard-go/internal/tgbot"
	"aliyun-guard-go/internal/web"
	"aliyun-guard-go/internal/xtime"
)

const version = "0.1.0-m7"

func main() {
	log.SetFlags(log.LstdFlags)
	paths := config.FromEnv()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if err := setupLogger(filepath.Dir(paths.Config)); err != nil {
		fmt.Fprintln(os.Stderr, "日誌檔案開啟失敗（僅輸出到終端）:", err)
	}

	switch os.Args[1] {
	case "run":
		// Telegram Bot 控制與網路面板隨守護行程背景啟動（對應 start_background()）；
		// 各自內部重讀配置，未啟用時靜默跳過。
		go func() {
			if err := tgbot.Run(paths); err != nil {
				log.Printf("Telegram Bot 控制結束: %v", err)
			}
		}()
		web.StartBackground(paths)
		if err := guard.RunDaemon(paths); err != nil {
			log.Fatalf("守護行程異常結束: %v", err)
		}
	case "once":
		runCycle(paths, false)
	case "dry-run":
		runCycle(paths, true)
	case "status":
		showStatus(paths)
	case "serve":
		if err := web.Serve(paths); err != nil {
			log.Fatalf("網路面板結束: %v", err)
		}
	case "bot":
		if err := tgbot.Run(paths); err != nil {
			log.Fatalf("Telegram Bot 結束: %v", err)
		}
	case "test-telegram":
		cfg, err := config.Load(paths.Config)
		must(err)
		if err := notify.TestMessage(cfg); err != nil {
			log.Fatalf("測試訊息發送失敗: %v", err)
		}
		fmt.Println("測試訊息已發送")
	case "version":
		fmt.Println("aliyun-guard-go", version)
	default:
		usage()
		os.Exit(2)
	}
}

// runCycle 執行單輪檢測（once / dry-run 共用）。
func runCycle(paths config.Paths, dryRun bool) {
	cfg, err := config.Load(paths.Config)
	must(err)
	st, err := state.Load(paths.State)
	must(err)
	results := guard.RunCycle(paths, cfg, st, dryRun, false)
	for _, r := range results {
		fmt.Printf("%s %s\n", r.Name, r.Message)
	}
}

// showStatus 輸出最近檢測狀態，對應 show_status() 的精簡版。
func showStatus(paths config.Paths) {
	cfg, err := config.Load(paths.Config)
	must(err)
	st, err := state.Load(paths.State)
	must(err)
	fmt.Println("调度后端: 手动（Go 版由 systemd 直接运行 run 子命令）")
	fmt.Printf("实例数量: %d\n", len(config.Users(cfg)))
	fmt.Printf("检测间隔: %d 秒\n", config.GetInt(cfg, "interval_seconds", 300))
	if finished, ok := st["last_cycle_finished_at"].(string); ok {
		fmt.Println("最近检测完成:", finished)
	}
	if started, ok := st["last_cycle_started_at"].(string); ok {
		fmt.Println("最近检测开始:", started)
	}
	if month, ok := st["cdt_month_checked"].(string); ok {
		fmt.Println("CDT 月结已检:", month, "（当前:", xtime.MonthKey(time.Now()), "）")
	}
}

// setupLogger 將日誌同時寫入終端與 logs/guard.log（每日輪轉、保留 14 天，
// 格式與上游 logging.Formatter 一致）。
func setupLogger(home string) error {
	logDir := filepath.Join(home, "logs")
	if env := os.Getenv("ALIYUN_GUARD_LOG_DIR"); env != "" {
		logDir = env
	}
	rot := logging.New(filepath.Join(logDir, "guard.log"), 14)
	guard.Logger = logging.NewLeveled(os.Stdout, rot)
	aliyun.LogWarning = guard.Logger.Warningf
	return nil
}

func usage() {
	fmt.Print(`用法: aliyun-guard-go [命令]

run           守护模式（systemd 启动此项）
once          立即执行一轮检测
dry-run       演练一轮，不执行开关机
status        查看最近检测状态
serve         启动网页控制面板（需 web_panel.enabled 且已设置登录密码）
bot           单独启动 Telegram Bot 控制（run 模式已内置）
test-telegram 发送 Telegram 测试消息
version       显示版本号
`)
}

func must(err error) {
	if err != nil {
		log.Fatalf("致命错误: %v", err)
	}
}
