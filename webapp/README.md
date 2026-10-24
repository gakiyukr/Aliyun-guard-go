# webapp — 網路面板前端

Nuxt 4 + shadcn-vue + Tailwind 4，**SPA 模式**（`ssr: false`）。
建置產物 `dist/`（gitignore，不進倉）由 `embed.go` 以 `go:embed all:dist`
內嵌進 Go binary——生產環境不需要 Node；Release binary 由 CI 建置。

## 開發

```bash
pnpm install
pnpm dev        # /api 經 devProxy 轉發到本機 Go 服務 127.0.0.1:8765
```

先在本機啟動 Go 後端（`go run ./cmd/guard serve`）再開 dev server。

## 建置

```bash
pnpm generate && node scripts/sync-dist.mjs   # .output/public → dist/
```

`dist/` 是實體目錄（提交進倉）；Go 側 embed 路徑用 `path.Clean` 匹配
slash 路徑，勿改用 `filepath.Clean`（Windows 反斜線會破壞匹配）。

## 約定

- 面向用戶字串一律**繁體中文**
- 後端 API 契約不動：session cookie、CSRF（`X-CSRF-Token`）、
  `{"ok":bool,"error":msg}` 回應格式
- 共用客戶端在 `app/composables/useApi.ts`（api/pollJob/csrfToken）；
  確認框走 `useConfirm` + `components/ConfirmHost.vue`
- 模組頂層禁止 `useState`（脫離 Nuxt 實例會白屏），用模組級 `ref`
