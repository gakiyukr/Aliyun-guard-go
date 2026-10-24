// Package webapp 內嵌 Nuxt SPA 建置產物（webapp/dist，由 `pnpm generate`
// 與 scripts/sync-dist.mjs 同步）。Go 端零依賴原則不變：前端為獨立
// 工具鏈，產物以靜態檔案形式提交並 embed 進 binary。
package webapp

import (
	"embed"
)

// Dist 為 SPA 建置產物（index.html + _nuxt/ hashed assets）。
//
//go:embed all:dist
var Dist embed.FS
