// 服務重啟的 Linux 實作，對應 web_actions.service_command("restart")：
// 容器模式（ALIYUN_GUARD_CONTAINER=1）以脫離會話的子程序延遲殺死 PID 1；
// APP_DIR 存在 control.sh 時交由腳本重啟（與上游逐字相同的錯誤訊息）；
// 兩者皆否（Go 版單二進位部署形態）則退回直接 systemctl 重啟本服務。
//go:build linux

package web

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// serviceRestart 依部署形態擇一重啟服務；子程序以 Setsid 脫離會話，
// 確保本服務被 systemd 停止時重啟動作不被一併終止（對應上游 detached_process）。
func serviceRestart(appDir string) error {
	if os.Getenv("ALIYUN_GUARD_CONTAINER") == "1" {
		cmd := exec.Command("/bin/sh", "-c", "sleep 1; kill -TERM 1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("容器重启请求失败: %w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	control := filepath.Join(appDir, "control.sh")
	if _, err := os.Stat(control); err == nil {
		cmd := exec.Command(control, "restart")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("服务重启失败: %w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	// Go 版部署無 control.sh：systemd unit 即本服務，直接重啟。
	if err := exec.Command("systemctl", "restart", "aliyun-guard.service").Run(); err != nil {
		return fmt.Errorf("服务重启失败: %w", err)
	}
	return nil
}
