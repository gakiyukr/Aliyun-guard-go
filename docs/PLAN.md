# 改寫計劃與相容性契約

所有回覆、註釋、文檔使用繁體中文（依 CLAUDE.md）；提交訊息使用英文。

## 里程碑

| 里程碑 | 內容 | 狀態 |
|--------|------|------|
| **M1 核心閉環** | config/state 相容層、RPC 簽名客戶端（ECS/CDT/BSS）、排程、check_one 決策引擎、run_cycle、守護迴圈（分鐘邊界對齊）、Telegram 通知、CLI（run/once/dry-run/status/test-telegram） | ✅ 本輪完成 |
| **M1.5 精修** | 帳單快取（billing_cache，state 結構含 next_refresh_at 與清理邏輯）、notify 訊息格式與上游逐字對齊、ECS 批量狀態查詢（query_instance_statuses + 預算限制）、單元測試（排程/月結算/決策表） | ✅ 完成（測試全綠） |
| **M2 完整 Bot** | 對等移植 telegram_control.py（1,360 行）：單訊息面板、inline 鍵盤導航、危險操作二次確認、每實例控制、訂閱管理捨棄（屬 proxy 模組） | ✅ 完成（測試全綠） |
| **M3 網路面板對等** | 登入（PBKDF2-SHA256）、CSRF、HttpOnly Session、登入限流、web_actions.py 全部 API 對等移植、web_panel.html 裁剪 proxy/備份相關 UI | ✅ |
| **M4 打包部署** | lumberjack 日誌輪轉（對應每日輪轉保留 14 天）、systemd unit 產生器、交叉編譯產物、部署到 VPS 並與 Python 版並行驗證後切換 | ✅ 2026-09-10 已部署上線 |
| **M5 模組補回** | S3 備份（手寫 SigV4 客戶端 internal/s3 + 編排 internal/s3backup）、加密備份/還原（internal/backup，手寫 PBKDF2，信封與 Python 互通）、實例探索（internal/aliyun/discovery.go）、面板路由與守護迴圈掛點 | ✅ 2026-09-10 完成（測試全綠） |
| **M6 面板 UI 重寫** | 以 Nuxt 4 + shadcn-vue 重寫面板（webapp/，SPA 模式 + go:embed 單 binary 部署不變）；全部功能對等、直接取代 web_panel.html；後端 API 契約零變動（僅新增 SPA fallback 靜態服務） | ✅ 2026-09-11 完成（單元測試 + 本機端到端驗證；VPS 部署待使用者確認） |

## 已放棄模組（依需求裁決）

- **sing-box 節點代理**：telegram_proxy.py（919 行）整體放棄；config 中
  connection_mode/node_urls/subscription_* 欄位原樣保留但不讀取。
- 回滾快照、程式更新、看門狗行程、訂閱節點管理：單機手動部署形態下確認不納入。
- 15k 行 install.sh：Go 單 binary + 30 行 systemd unit 取代。

## 相容性契約（不可破壞）

1. **config.json**：map 基底讀寫，未知欄位回寫不流失；權限 600；原子寫入（tmp+rename）。
2. **state.json**：`last_cycle_epoch`（epoch 秒）與 `last_cycle_started_at` /
   `last_cycle_finished_at`（ISO 8601 帶偏移）雙格式並存；`instances` 每實例保留
   `schedule_signature` / `schedule_target`（排程遷移偵測依賴此指紋）。
3. **月結算三級 fallback**：`cdt_month_checked` 字串 → ISO 時間戳 → epoch，
   缺一即可能誤觸補檢（多查一次帳單無害，但要在驗證清單中）。
4. **時間語意**：CDT 月結算固定 UTC+8（`xtime.CDT`）；每日排程以 `schedule.timezone`
   指定的時區解讀（Go 版新增，預設 `Asia/Taipei`，可為 IANA 名稱、`UTC+8` 偏移或
   `system`；IANA 資料庫內嵌於 binary，主機缺 zoneinfo 亦可解析），日誌/通知用
   系統本地時區。排程指紋含時區（`start|stop|tz`），變更時區觸發一次排程遷移；
   舊版指紋格式（`start|stop`）升級後同樣觸發一次遷移以對齊目標狀態。
   主機改時區後需重啟服務（Go `time.Local` 快取行為與 Python 一致）。
5. **排程跨午夜**：start >= stop 視為跨夜區間（`current >= start || current < stop`）。
6. **決策優先級**：暫停 > 排程關機時段（僅依賴 ECS 狀態，CDT/BSS 失敗不阻擋關機）
   > CDT/ECS 核心查詢失敗不動作 > 流量閾值規則。
7. **CLI 訊息**：面向用戶的中文字串與 Python 版逐字一致（面板與 Bot 文案相容）。

## 已知近似（餘留事項）

- M1.5 已補齊：帳單快取（含失敗回退與清理）、ECS 批量查詢（同憑證同 region 一次
  DescribeInstances）、決策訊息逐字對齊、單元測試（xtime/schedule/cycle/billing/check
  共 20+ 案例，決策表含 dry-run、排程關機優先、核心查詢失敗阻擋、輪詢確認/等待措辭）。
- M3 裁剪中的 S3 備份、加密備份/還原、實例探索已於 M5 補回（見里程碑表）。
  仍維持停用佔位的欄位：`nodes: []`、`rollback_snapshots: []`（程式更新/回滾
  快照已裁決放棄），前端對應區塊顯示為停用，無需改動 2396 行 JS。
- 主日誌等級：已於 2026-09-10 補齊——`guard.Logger` 改為三級 `LevelLogger`
  （INFO/WARNING/ERROR，`logging.LeveledLogger`），呼叫點等級逐項對應上游
  `LOGGER.info/warning/error`；CDT 重試警告亦逐字對齊。
- per-instance 日誌已於 M4 補齊（internal/logging 輪轉 + guard.WriteInstanceLog，
  欄位格式與脫敏對齊上游），面板 `/api/logs?instance=N` 已可讀取。
- `/api/service/restart` 與 web-settings 觸發的重啟：已對齊 `service_command`——
  容器模式殺 PID 1、APP_DIR 有 control.sh 走腳本，否則回退 systemctl 重啟
  `aliyun-guard.service`（Go 部署形態無 control.sh）；非 Linux 僅記錄日誌。

## 已知擴充（偏離上游之處）

- `telegram.allow_group_control`（預設 false = 上游行為僅私聊）：設為 true 時
  放行群組中的管理員指令（含 @BotName 後綴過濾），供群組部署場景使用。
- 手動開關機入口 `guard.ControlInstance`（對應 web_panel.control_instance）
  為 Bot（M2）與網路面板（M3）共用，日誌路徑合併為主日誌。

## 驗證計劃（切換前必過）

1. `dry-run` 與 Python 版同輪輸出逐項比對（流量/狀態/決策動作）。✅ 2026-09-10
2. 排程邊界：人工把 start_time 設為 2 分鐘後，確認 Go 版於邊界分鐘內觸發開機。✅
   2026-09-11 00:30 KR/HK-CDT 計劃關機準時觸發（journalctl 驗證）。
3. 跨月模擬：修改 state 的 `cdt_month_checked` 為上月，確認補檢觸發一次且不重複。
   ——使用者裁決忽略（三級 fallback 邏輯已有單元測試覆蓋）。
4. 相容性：直接掛上 Python 版現行 config.json/state.json 跑 24 小時，對比 state 差異。
   ✅ 2026-09-11 Go 版無異常運行超過 24 小時，排程關機行為與通知與 Python 版一致。
5. 面板加密備份/還原實測：✅ 2026-09-11 使用者於面板操作驗證通過。
