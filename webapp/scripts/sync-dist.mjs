// 同步 Nuxt 建置產物到 webapp/dist（供 go:embed）。
// 用法：pnpm generate 之後執行 `node scripts/sync-dist.mjs`。
import { cpSync, rmSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'

const src = resolve(import.meta.dirname, '../.output/public')
const dest = resolve(import.meta.dirname, '../dist')

if (!existsSync(src)) {
  console.error('找不到 .output/public，請先執行 `pnpm generate`')
  process.exit(1)
}
rmSync(dest, { recursive: true, force: true })
cpSync(src, dest, { recursive: true })
console.log('已同步 .output/public → dist')
