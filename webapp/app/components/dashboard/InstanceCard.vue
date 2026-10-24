<script setup lang="ts">
// 實例卡片：對應舊面板 instanceCard()——狀態徽章、電源按鈕、單機操作
// 下拉選單、指標網格、流量進度條、sparkline、下一動作與結果行。
import { fmtNum, fmtDate, statusInfo } from '~/composables/useFormat'

const props = defineProps<{
  item: any
  cdtResetAt: string
}>()

const emit = defineEmits<{
  action: [action: string, item: any]
}>()

const status = computed(() => statusInfo(props.item.status, props.item.paused))
const percent = computed(() => {
  if (props.item.traffic_percent === null || props.item.traffic_percent === undefined) return 0
  return Math.max(0, Math.min(100, props.item.traffic_percent))
})
const barClass = computed(() => (percent.value >= 100 ? 'bg-destructive' : percent.value >= 85 ? 'bg-amber-500' : 'bg-primary'))
const bill = computed(() => {
  const item = props.item
  if (!item.billing_enabled) return '已關閉'
  if (item.bill_amount === null || item.bill_amount === undefined) {
    return item.bill_error ? '查詢失敗' : '--'
  }
  return `${item.bill_symbol || ''}${fmtNum(item.bill_amount)} ${item.bill_currency || ''}`
})
const billTitle = computed(() => {
  if (!props.item.bill_checked_at) return ''
  return `賬單資料時間：${fmtDate(props.item.bill_checked_at)}${props.item.bill_from_cache ? '（緩存）' : ''}${props.item.bill_error ? `；${props.item.bill_error}` : ''}`
})
const sched = computed(() =>
  props.item.schedule.enabled
    ? `${props.item.schedule.start_time} - ${props.item.schedule.stop_time}${props.item.schedule.timezone ? `（${props.item.schedule.timezone}）` : ''}`
    : '已關閉')
const next = computed(() =>
  props.item.schedule.next_at
    ? `${props.item.schedule.next_action === 'start' ? '開機' : '關機'} ${fmtDate(props.item.schedule.next_at)}`
    : '無計劃')
const resetText = computed(() => {
  const resetMs = props.cdtResetAt ? new Date(props.cdtResetAt).getTime() - Date.now() : NaN
  if (!Number.isFinite(resetMs)) return '--'
  const days = Math.max(0, Math.floor(resetMs / 86400000))
  const hours = Math.max(0, Math.floor((resetMs % 86400000) / 3600000))
  return `${days}天${String(hours).padStart(2, '0')}小時`
})
const powerAction = computed(() => (props.item.status === 'Running' ? 'stop' : 'start'))
const powerLabel = computed(() => (powerAction.value === 'start' ? '開機' : '關機'))
const transitioning = computed(() => ['Starting', 'Stopping', 'Pending'].includes(props.item.status))

const menuItems = computed(() => [
  { action: 'schedule', icon: 'i-lucide-calendar-clock', label: '定時計劃' },
  {
    action: 'pause',
    icon: props.item.paused ? 'i-lucide-play' : 'i-lucide-pause',
    label: props.item.paused ? '恢復監控' : '暫停監控',
    danger: false,
  },
  { action: 'edit', icon: 'i-lucide-pencil', label: '編輯配置', danger: false },
  { action: 'validate', icon: 'i-lucide-shield-check', label: '只讀校驗', danger: false },
  { action: 'logs', icon: 'i-lucide-terminal', label: `查看獨立日誌${props.item.instance_log_enabled ? '（已啟用）' : ''}`, danger: false },
  { action: 'delete', icon: 'i-lucide-trash-2', label: '刪除監控實例', danger: true },
])

// sparkline：與舊面板 SVG 幾何一致（320x58 viewbox）
const spark = computed(() => {
  const points = props.item.history
  if (!points || !points.length) return null
  const values = points.map((p: any) => Number(p.value))
  const min = Math.min(...values)
  const max = Math.max(...values)
  const span = Math.max(max - min, 0.01)
  const chartLeft = 4
  const chartRight = 316
  const chartTop = 10
  const chartBottom = 48
  const coords = values.map((value: number, i: number) => {
    const x = values.length === 1 ? 160 : (i / (values.length - 1)) * (chartRight - chartLeft) + chartLeft
    const y = chartBottom - ((value - min) / span) * (chartBottom - chartTop)
    return { x: Number(x.toFixed(1)), y: Number(y.toFixed(1)) }
  })
  const polyline = coords.map((p: any) => `${p.x},${p.y}`).join(' ')
  const area = `M ${coords[0].x} ${chartBottom} L ${coords.map((p: any) => `${p.x} ${p.y}`).join(' L ')} L ${coords[coords.length - 1].x} ${chartBottom} Z`
  return { polyline, area, coords }
})
</script>

<template>
  <Card class="gap-4 py-4">
    <CardHeader class="px-4">
      <div class="flex items-start justify-between gap-2">
        <div class="min-w-0">
          <CardTitle class="truncate text-base">{{ item.name }}</CardTitle>
          <CardDescription class="truncate">{{ item.region }} · {{ item.instance_id }}</CardDescription>
        </div>
        <div class="flex items-center gap-2 shrink-0">
          <Badge
            :variant="status.kind === 'running' ? 'default' : status.kind === 'stopped' ? 'destructive' : 'outline'"
          >
            {{ status.label }}
          </Badge>
          <Button
            :variant="powerAction === 'start' ? 'default' : 'secondary'"
            size="sm"
            :disabled="transitioning"
            @click="emit('action', 'power', item)"
          >
            <Icon name="i-lucide-power" class="size-4" />
            {{ powerLabel }}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <Button variant="ghost" size="icon" title="單機設定">
                <Icon name="i-lucide-more-horizontal" class="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                v-for="mi in menuItems"
                :key="mi.action"
                :class="mi.danger ? 'text-destructive' : ''"
                @click="emit('action', mi.action, item)"
              >
                <Icon :name="mi.icon" class="size-4" />
                {{ mi.label }}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </CardHeader>
    <CardContent class="px-4">
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
        <div :title="`北京時間 ${fmtDate(cdtResetAt)} 自動額外檢測一次`">
          <div class="text-xs text-muted-foreground">額度重置</div>
          <div class="text-sm font-semibold">{{ resetText }}</div>
        </div>
        <div>
          <div class="text-xs text-muted-foreground">CDT 流量</div>
          <div class="text-sm font-semibold">{{ fmtNum(item.traffic_gb) }} GB</div>
        </div>
        <div>
          <div class="text-xs text-muted-foreground">關機閾值</div>
          <div class="text-sm font-semibold">{{ fmtNum(item.traffic_limit_gb) }} GB</div>
        </div>
        <div :title="billTitle">
          <div class="text-xs text-muted-foreground">本月賬單</div>
          <div class="text-sm font-semibold">{{ bill }}</div>
        </div>
        <div>
          <div class="text-xs text-muted-foreground">每日計劃</div>
          <div class="text-sm font-semibold">{{ sched }}</div>
        </div>
      </div>

      <div class="mt-4">
        <div class="flex items-center justify-between text-xs">
          <span class="text-muted-foreground">本月流量使用率</span>
          <strong>{{ item.traffic_percent === null || item.traffic_percent === undefined ? '--' : `${fmtNum(item.traffic_percent, 1)}%` }}</strong>
        </div>
        <div class="mt-1.5 h-2 w-full overflow-hidden rounded-full bg-muted">
          <div :class="barClass" class="h-full rounded-full transition-all" :style="{ width: `${percent}%` }" />
        </div>
      </div>

      <div class="relative mt-3 h-[58px]">
        <svg v-if="spark" viewBox="0 0 320 58" preserveAspectRatio="none" class="h-full w-full" aria-hidden="true">
          <line x1="4" y1="48" x2="316" y2="48" class="stroke-border" stroke-width="1" />
          <path :d="spark.area" class="fill-primary/10" />
          <polyline v-if="spark.coords.length > 1" :points="spark.polyline" fill="none" class="stroke-primary" stroke-width="1.5" />
          <circle
            :cx="spark.coords[spark.coords.length - 1].x"
            :cy="spark.coords[spark.coords.length - 1].y"
            r="2.5"
            class="fill-primary"
          />
        </svg>
        <div v-else class="flex h-full items-center justify-center text-xs text-muted-foreground">
          等待流量樣本
        </div>
      </div>

      <div class="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon name="i-lucide-calendar-clock" class="size-3.5" />
        <span>下一動作：{{ next }}</span>
      </div>
      <div class="mt-2 border-t pt-2 text-xs text-muted-foreground">
        {{ item.message }}
      </div>
    </CardContent>
  </Card>
</template>
