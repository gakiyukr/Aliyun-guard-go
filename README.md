# aliyun-guard-go

aliyun-guard（Python 版 aliyun_guard.py v1.6.21）的**零依賴 Go 改寫**：
阿里雲 ECS CDT 流量保活與自動止損守護——閾值決策 → 自動開關機 →
Telegram 通知與控制 + 網頁控制台。

**已在生產 VPS 上線運行（2026-09-10 起）**，Python 版停用。

## 功能總覽

- **保活閉環**：CDT 流量 / ECS 狀態 / BSS 賬單週期查詢，按閾值自動開關機止損
- **每日定時開關機**：跨午夜區間、排程遷移偵測、月結算三級 fallback（CDT 固定 UTC+8）；
  排程時區可設定（`schedule.timezone`，預設 `Asia/Taipei`，不依賴部署機器時區）
- **Telegram**：檢測匯總通知 + 控制 Bot（單訊息面板、inline 鍵盤、危險操作二次確認）
- **網頁控制台**：Nuxt 4 + shadcn-vue SPA（繁體中文），登入認證 + CSRF + 登入限流，
  實例卡（流量/sparkline/賬單/排程）、立即檢測、實例管理、自動發現、日誌、設定
- **備份**：加密備份/還原（PBKDF2 + AES-256-GCM，與 Python 版信封互通）、
  S3 自動備份（手寫 SigV4）、實例自動發現
- **部署形態**：單一靜態 binary（前端 dist 以 `go:embed` 內嵌），systemd 直接跑；
  推送 main 由 GitHub Actions 自動建置並發 Release（版本取自 `cmd/guard/main.go`
  的 `version` 常數，升版本時改該常數即可）

## 相容性契約

config.json / state.json 與 Python 版**逐字互通**（map 基底讀寫、未知欄位不流失、
原子寫入），可來回切換零轉換。完整契約清單與已知近似見
[docs/PLAN.md](docs/PLAN.md)。

## 目錄結構

```
cmd/guard/            CLI 入口（run/once/dry-run/status/serve/bot/test-telegram）
internal/
  aliyun/             阿里雲 RPC 簽名客戶端（ECS/CDT/BSS，HMAC-SHA1 純手寫）+ 實例探索
  backup/             加密備份/還原信封（手寫 PBKDF2 + AES-256-GCM）
  s3/                 手寫 AWS SigV4 S3 客戶端
  s3backup/           S3 備份編排（排程 slot 狀態機、flock）
  config/             config.json 相容層
  state/              state.json 相容層
  guard/              核心：決策引擎、run_cycle、排程、手動開關機、per-instance 日誌
  tgbot/              Telegram 控制 Bot
  notify/             Telegram 通知
  web/                網路面板後端（auth/CSRF/完整 API + go:embed 服務 Nuxt SPA）
  logging/            每日輪轉日誌（保留 14 天）
  xtime/              時間語意（CDT 固定 UTC+8、排程時區可設定預設 Asia/Taipei、
                      其餘系統本地時區；內嵌 tzdata 由 cmd/guard 引入）
webapp/               面板前端（Nuxt 4 + shadcn-vue + Tailwind 4，SPA 模式）
deploy/               systemd unit 模板
docs/PLAN.md          里程碑、相容性契約、驗證計劃
```

## 建置

Go 端**零第三方套件**（純標準庫）；前端工具鏈僅開發機需要，產物
`webapp/dist` 已提交進倉，clone 後直接可建 Go binary：

```bash
go build ./cmd/guard                                   # 本機建置（含前端 embed）
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w" -o dist/aliyun-guard-go ./cmd/guard  # Linux 生產 binary
```

### 前端開發（僅修改 UI 時需要）

```bash
cd webapp
pnpm install            # Node ≥ 20；Windows 下 Node 工具鏈請用 PowerShell 跑
pnpm dev                # dev server，/api 自動代理到本機 Go 服務 127.0.0.1:8765
pnpm generate && node scripts/sync-dist.mjs   # 重建產物到 dist/（提交進倉）
```

## 部署

生產環境：VPS `/opt/aliyun-guard`，systemd `aliyun-guard.service`。

1. 交叉編譯 Linux binary
2. 上傳 `/tmp` → `cp aliyun-guard-go aliyun-guard-go.bak`（回滾保險）
   → `mv -f` 原子替換 → `systemctl restart aliyun-guard`
3. `journalctl -u aliyun-guard` 驗證啟動

`ALIYUN_GUARD_HOME` 預設 `/opt/aliyun-guard`，直接沿用現有 config.json /
state.json（相容性契約見 docs/PLAN.md）。詳見 [deploy/](deploy/)。

## 測試

```bash
go build ./... && go vet ./... && gofmt -l . && go test ./...   # 提交前四連
```

單元測試不打真實 API（stub 模式，見 `internal/guard/check_test.go`）。
驗證歷史（排程邊界、24 小時運行、備份還原實測等）見 docs/PLAN.md 驗證計劃。

## 授權

[MIT](LICENSE)
