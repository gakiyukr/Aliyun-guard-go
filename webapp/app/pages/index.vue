<script setup lang="ts">
// 總覽頁：對應舊面板 dashboard tab——摘要卡、服務狀態與通知、立即檢測/
// 演練檢測（job 輪詢）、CDT 重置倒數、實例卡片列表；15 秒自動刷新。
// 實例管理（新增/編輯/排程/發現/validate/delete/pause/power）同頁處理，
// 對應舊面板單頁形態（instanceDialog/scheduleDialog/discoveryDialog）。
import { toast } from 'vue-sonner'
import { api, errorMessage, pollJob } from '~/composables/useApi'
import { confirmAction } from '~/composables/useConfirm'
import { fmtDate, fmtNum } from '~/composables/useFormat'

const dashboard = ref<any>(null)
const management = ref<any>(null)
const runningJob = computed(() => Boolean(dashboard.value?.job?.running))

async function loadAll(notifyError = true) {
  if (document.hidden) return
  try {
    const [d, m] = await Promise.all([api('/api/dashboard'), api('/api/management')])
    dashboard.value = d
    management.value = m
  } catch (e: any) {
    if (notifyError) toast.error(errorMessage(e))
  }
}

async function runCheck(dryRun: boolean) {
  if (runningJob.value) return
  try {
    await api('/api/run', { method: 'POST', body: { dry_run: dryRun } })
    await pollJob((job) => {
      if (dashboard.value) dashboard.value.job = job
    })
    const job = await api('/api/job')
    if (job?.error) {
      toast.error(`手動檢測：${job.error}`)
    } else {
      toast.success(dryRun ? '演練檢測完成' : '檢測完成')
    }
    await loadAll(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
    await loadAll(false)
  }
}

async function refreshBilling() {
  try {
    await api('/api/billing/refresh', { method: 'POST', body: {} })
    await pollJob()
    toast.success('賬單刷新完成')
    await loadAll(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}

let timer: ReturnType<typeof setInterval> | null = null
onMounted(() => {
  loadAll()
  timer = setInterval(() => { if (!document.hidden) loadAll(false) }, 15000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

// --- 實例新增/編輯對話框 ---
const instanceDialogOpen = ref(false)
const instanceIndex = ref<number | null>(null)
const form = reactive({
  name: '', ak: '', sk: '', region: '', instance_id: '',
  traffic_limit_gb: 180, actions_enabled: true, instance_log_enabled: false,
  billing_enabled: true, billing_site: 'china', billing_endpoint: '', billing_region: '',
  billing_currency_code: '', billing_currency_symbol: '',
  schedule_enabled: false, schedule_start: '08:00', schedule_stop: '23:00', schedule_tz: 'Asia/Taipei',
})

// 排程時區候選：IANA 名稱（後端另支援任意 IANA 區域與 UTC+8 偏移寫法）；
// system 表示跟隨伺服器系統時區。
const scheduleTimezones = [
  'Asia/Taipei', 'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Macau',
  'Asia/Tokyo', 'Asia/Singapore', 'Asia/Seoul', 'UTC', 'system',
]
const formError = ref('')

function openInstanceDialog(index: number | null) {
  const existing = index === null ? null : management.value?.instances.find((i: any) => i.index === index)
  instanceIndex.value = index
  form.name = existing?.name || ''
  form.ak = ''
  form.sk = ''
  form.region = existing?.region || ''
  form.instance_id = existing?.instance_id || ''
  form.traffic_limit_gb = existing?.traffic_limit_gb ?? 180
  form.actions_enabled = existing?.actions_enabled ?? true
  form.instance_log_enabled = existing?.instance_log_enabled ?? false
  form.billing_enabled = existing?.billing?.enabled ?? true
  form.billing_site = existing?.billing?.site || 'china'
  form.billing_endpoint = existing?.billing?.endpoint || ''
  form.billing_region = existing?.billing?.region || ''
  form.billing_currency_code = existing?.billing?.currency_code || ''
  form.billing_currency_symbol = existing?.billing?.currency_symbol || ''
  form.schedule_enabled = existing?.schedule?.enabled ?? false
  form.schedule_start = existing?.schedule?.start_time || '08:00'
  form.schedule_stop = existing?.schedule?.stop_time || '23:00'
  form.schedule_tz = existing?.schedule?.timezone || 'Asia/Taipei'
  formError.value = ''
  instanceDialogOpen.value = true
}

function instancePayload(forceSave = false) {
  return {
    name: form.name.trim(),
    ak: form.ak.trim(),
    sk: form.sk.trim(),
    region: form.region.trim(),
    instance_id: form.instance_id.trim(),
    traffic_limit_gb: Number(form.traffic_limit_gb),
    actions_enabled: form.actions_enabled,
    instance_log_enabled: form.instance_log_enabled,
    billing: {
      enabled: form.billing_enabled,
      site: form.billing_site,
      endpoint: form.billing_endpoint.trim(),
      region: form.billing_region.trim(),
      currency_code: form.billing_currency_code.trim(),
      currency_symbol: form.billing_currency_symbol.trim(),
    },
    schedule: {
      enabled: form.schedule_enabled,
      start_time: form.schedule_start,
      stop_time: form.schedule_stop,
      timezone: form.schedule_tz,
    },
    force_save: forceSave,
  }
}

async function saveInstance(forceSave = false) {
  const path = instanceIndex.value === null ? '/api/instances' : `/api/instances/${instanceIndex.value}`
  const data = await api<any>(path, { method: 'POST', body: instancePayload(forceSave) })
  instanceDialogOpen.value = false
  toast.success('實例配置已保存')
  await loadAll(false)
}

async function onInstanceSubmit() {
  try {
    await saveInstance(false)
  } catch (e: any) {
    if (e?.status === 422) {
      const force = await confirmAction({
        title: '校驗失敗',
        message: '實例校驗失敗。仍然保存並讓定時檢測繼續報告錯誤？',
        confirmText: '仍然保存',
        tone: 'warning',
      })
      if (force) {
        try {
          await saveInstance(true)
        } catch (forceError: any) {
          formError.value = errorMessage(forceError)
        }
      } else {
        formError.value = errorMessage(e)
      }
    } else {
      formError.value = errorMessage(e)
    }
  }
}

// --- 定時計劃對話框 ---
const scheduleDialogOpen = ref(false)
const scheduleIndex = ref<number | null>(null)
const scheduleForm = reactive({ enabled: false, start: '08:00', stop: '23:00', tz: 'Asia/Taipei' })

function openScheduleDialog(item: any) {
  scheduleIndex.value = item.index
  scheduleForm.enabled = item.schedule.enabled
  scheduleForm.start = item.schedule.start_time
  scheduleForm.stop = item.schedule.stop_time
  scheduleForm.tz = item.schedule.timezone || 'Asia/Taipei'
  scheduleDialogOpen.value = true
}

async function saveSchedule() {
  if (scheduleForm.enabled && scheduleForm.start === scheduleForm.stop) {
    toast.error('開機時間和關機時間不能相同')
    return
  }
  try {
    await api(`/api/instances/${scheduleIndex.value}/schedule`, {
      method: 'POST',
      body: { enabled: scheduleForm.enabled, start_time: scheduleForm.start, stop_time: scheduleForm.stop, timezone: scheduleForm.tz },
    })
    scheduleDialogOpen.value = false
    toast.success('定時計劃已保存')
    await loadAll(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}

// --- 卡片操作 ---
async function onAction(action: string, item: any) {
  if (action === 'edit') {
    openInstanceDialog(item.index)
    return
  }
  if (action === 'schedule') {
    openScheduleDialog(item)
    return
  }
  if (action === 'validate') {
    try {
      const data = await api<any>(`/api/instances/${item.index}/validate`, { method: 'POST', body: {} })
      const r = data.result
      const details: string[] = []
      if (r.traffic_gb !== null && r.traffic_gb !== undefined) details.push(`CDT ${fmtNum(r.traffic_gb)} GB`)
      if (r.status) details.push(`ECS ${r.status}`)
      if (r.billing_enabled && r.bill_amount !== null && r.bill_amount !== undefined) details.push(`賬單 ${fmtNum(r.bill_amount)} ${r.bill_currency || ''}`)
      if (r.errors?.length) details.push(r.errors.join('；'))
      const text = `${r.ok ? '校驗成功' : '校驗失敗'}${details.length ? '：' + details.join(' · ') : ''}`
      if (r.ok) toast.success(text)
      else toast.warning(text)
    } catch (e: any) {
      toast.error(errorMessage(e))
    }
    return
  }
  if (action === 'delete') {
    const ok = await confirmAction({
      title: '刪除監控實例',
      message: `確認刪除 ${item.name} (${item.instance_id})？此操作不會刪除阿里雲 ECS。`,
      confirmText: '刪除',
      tone: 'warning',
    })
    if (!ok) return
    try {
      await api(`/api/instances/${item.index}/delete`, { method: 'POST', body: { instance_id: item.instance_id } })
      toast.success('監控實例已刪除')
      await loadAll(false)
    } catch (e: any) {
      toast.error(errorMessage(e))
    }
    return
  }
  if (action === 'pause') {
    const paused = !item.paused
    const ok = await confirmAction({
      title: paused ? '暫停監控' : '恢復監控',
      message: `確認${paused ? '暫停' : '恢復'} ${item.name}？`,
      confirmText: paused ? '暫停' : '恢復',
      tone: paused ? 'warning' : 'primary',
    })
    if (!ok) return
    try {
      await api(`/api/instances/${item.index}/pause`, { method: 'POST', body: { paused } })
      toast.success(paused ? '實例監控已暫停' : '實例監控已恢復')
      await loadAll(false)
    } catch (e: any) {
      toast.error(errorMessage(e))
    }
    return
  }
  if (action === 'power') {
    const act = item.status === 'Running' ? 'stop' : 'start'
    const ok = await confirmAction({
      title: act === 'start' ? '開機實例' : '關機實例',
      message: `確認${act === 'start' ? '開機' : '關機'} ${item.name}？`,
      confirmText: act === 'start' ? '開機' : '關機',
      tone: act === 'start' ? 'primary' : 'warning',
    })
    if (!ok) return
    try {
      const data = await api<any>(`/api/instances/${item.index}/power`, { method: 'POST', body: { action: act } })
      toast.success(data.result?.notification_error
        ? `操作成功，Telegram 通知失敗：${data.result.notification_error}`
        : '實例操作已完成')
      await loadAll(false)
    } catch (e: any) {
      toast.error(errorMessage(e))
    }
    return
  }
  if (action === 'logs') {
    navigateTo({ path: '/logs', query: { instance: String(item.index) } })
  }
}

// --- 自動發現對話框 ---
const discoveryDialogOpen = ref(false)
const discovery = reactive({
  ak: '', sk: '', regions: '', tag_key: '', tag_value: '',
  traffic_limit_gb: 180, billing_site: 'china', actions_enabled: true,
})
const discovered = ref<any[]>([])
const discoveryMsg = ref('')
const discoveryMsgError = ref(false)
const scanning = ref(false)
const importing = ref(false)
const discoveryCredentials = ref<{ ak: string, sk: string } | null>(null)
const selected = ref<Set<number>>(new Set())

function openDiscovery() {
  discovered.value = []
  discoveryCredentials.value = null
  discovery.ak = ''
  discovery.sk = ''
  discoveryMsg.value = ''
  discoveryDialogOpen.value = true
}

async function scanInstances() {
  scanning.value = true
  discoveryMsg.value = '正在跨 Region 掃描 ECS...'
  discoveryMsgError.value = false
  try {
    const data = await api<any>('/api/discovery/scan', {
      method: 'POST',
      body: {
        ak: discovery.ak.trim(),
        sk: discovery.sk.trim(),
        regions: discovery.regions.trim(),
        tag_key: discovery.tag_key.trim(),
        tag_value: discovery.tag_value.trim(),
      },
    })
    discovered.value = data.result.instances
    discoveryCredentials.value = { ak: discovery.ak.trim(), sk: discovery.sk.trim() }
    discoveryMsg.value = `發現 ${data.result.instances.length} 臺 ECS；${data.result.errors.length} 個 Region 掃描失敗`
    discoveryMsgError.value = Boolean(data.result.errors.length)
    // 預選所有未配置實例
    selected.value = new Set(data.result.instances
      .map((it: any, i: number) => (it.already_configured ? -1 : i))
      .filter((i: number) => i >= 0))
  } catch (e: any) {
    discoveryMsg.value = errorMessage(e)
    discoveryMsgError.value = true
  } finally {
    scanning.value = false
  }
}

async function importInstances() {
  if (!discoveryCredentials.value) return
  const items = [...selected.value].map(i => discovered.value[i]).filter(Boolean)
  if (!items.length) {
    toast.error('請選擇至少一個實例')
    return
  }
  importing.value = true
  try {
    const data = await api<any>('/api/discovery/import', {
      method: 'POST',
      body: {
        ...discoveryCredentials.value,
        instances: items,
        traffic_limit_gb: Number(discovery.traffic_limit_gb),
        actions_enabled: discovery.actions_enabled,
        billing_site: discovery.billing_site,
      },
    })
    discoveryDialogOpen.value = false
    toast.success(`已導入 ${data.result.count} 臺 ECS`)
    await loadAll(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
  } finally {
    importing.value = false
  }
}

// --- 摘要 ---
const service = computed(() => dashboard.value?.service || {})
const serviceText = computed(() =>
  service.value.stale ? '檢測超時' : service.value.last_ok ? '服務正常' : '存在異常')
const serviceBad = computed(() => service.value.stale || service.value.last_error_count > 0)
const notices = computed(() => {
  const list: string[] = []
  const d = dashboard.value
  if (!d) return list
  if (d.service?.stale) list.push('後臺檢測超過預期間隔，請檢查服務狀態')
  if (d.service?.telegram_error) list.push(`Telegram：${d.service.telegram_error}`)
  if (d.job?.error) list.push(`手動檢測：${d.job.error}`)
  return list
})
const runningCount = computed(() =>
  (dashboard.value?.users || []).filter((x: any) => x.status === 'Running' && !x.paused).length)
const errorCount = computed(() =>
  (dashboard.value?.users || []).filter((x: any) => x.level === 'error').length)
const lastUpdated = computed(() =>
  `伺服器時間 ${fmtDate(dashboard.value?.now)} · 最後檢測 ${fmtDate(service.value.last_finished_at)}`)
</script>

<template>
  <div>
    <div v-if="!dashboard" class="flex items-center justify-center py-20 text-muted-foreground">
      <Icon name="i-lucide-loader-circle" class="mr-2 size-5 animate-spin" />
      載入中
    </div>
    <div v-else class="grid gap-4">
      <!-- 通知列 -->
      <div v-if="notices.length" class="rounded-lg border border-amber-500/50 bg-amber-500/10 px-4 py-2.5 text-sm text-amber-700 dark:text-amber-400">
        {{ notices.join('；') }}
      </div>

      <!-- 摘要 -->
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Card class="py-4">
          <CardContent class="px-4">
            <div class="text-xs text-muted-foreground">監控實例</div>
            <div class="text-2xl font-bold">{{ dashboard.users.length }}</div>
          </CardContent>
        </Card>
        <Card class="py-4">
          <CardContent class="px-4">
            <div class="text-xs text-muted-foreground">運行中</div>
            <div class="text-2xl font-bold">{{ runningCount }}</div>
          </CardContent>
        </Card>
        <Card class="py-4">
          <CardContent class="px-4">
            <div class="text-xs text-muted-foreground">異常實例</div>
            <div class="text-2xl font-bold">{{ errorCount }}</div>
          </CardContent>
        </Card>
        <Card class="py-4">
          <CardContent class="px-4">
            <div class="flex items-center justify-between">
              <div>
                <div class="text-xs text-muted-foreground">服務狀態</div>
                <div class="flex items-center gap-2">
                  <span class="inline-block size-2 rounded-full" :class="serviceBad ? 'bg-destructive' : 'bg-emerald-500'" />
                  <span class="text-2xl font-bold">{{ serviceText }}</span>
                </div>
                <div class="mt-1 text-xs text-muted-foreground">累計檢測 {{ service.cycle_count }} 輪 · {{ lastUpdated }}</div>
              </div>
              <Badge variant="outline">v{{ dashboard.version }}</Badge>
            </div>
          </CardContent>
        </Card>
      </div>

      <!-- 操作列 -->
      <div class="flex flex-wrap items-center gap-2">
        <Button :disabled="runningJob" @click="runCheck(false)">
          <Icon :name="runningJob ? 'i-lucide-loader-circle' : 'i-lucide-play'" :class="runningJob ? 'animate-spin' : ''" class="size-4" />
          {{ runningJob ? '檢測中' : '立即檢測' }}
        </Button>
        <Button variant="outline" :disabled="runningJob" @click="runCheck(true)">
          <Icon name="i-lucide-flask-conical" class="size-4" />
          演練檢測
        </Button>
        <Button variant="outline" :disabled="runningJob" @click="refreshBilling">
          <Icon name="i-lucide-receipt" class="size-4" />
          刷新賬單
        </Button>
        <Button @click="openInstanceDialog(null)">
          <Icon name="i-lucide-plus" class="size-4" />
          添加監控實例
        </Button>
        <Button variant="outline" @click="openDiscovery">
          <Icon name="i-lucide-search" class="size-4" />
          自動發現 ECS
        </Button>
      </div>

      <!-- 實例卡片 -->
      <div v-if="dashboard.users.length" class="grid gap-4 xl:grid-cols-2">
        <DashboardInstanceCard
          v-for="item in dashboard.users"
          :key="item.index"
          :item="item"
          :cdt-reset-at="dashboard.cdt_reset?.next_at"
          @action="onAction"
        />
      </div>
      <div v-else class="rounded-lg border border-dashed py-16 text-center text-muted-foreground">
        <Icon name="i-lucide-server" class="mx-auto mb-2 size-8" />
        <p>暫無監控實例</p>
      </div>

      <!-- 新增/編輯實例對話框 -->
      <Dialog v-model:open="instanceDialogOpen">
        <DialogContent class="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{{ instanceIndex === null ? '添加監控實例' : `編輯 ${form.name || '實例'}` }}</DialogTitle>
          </DialogHeader>
          <form class="grid gap-4" @submit.prevent="onInstanceSubmit">
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>實例名稱</Label>
                <Input v-model="form.name" required />
              </div>
              <div class="grid gap-2">
                <Label>Region ID</Label>
                <Input v-model="form.region" placeholder="cn-hongkong" required />
              </div>
              <div class="grid gap-2">
                <Label>實例 ID</Label>
                <Input v-model="form.instance_id" required />
              </div>
              <div class="grid gap-2">
                <Label>關機閾值（GB）</Label>
                <Input v-model="form.traffic_limit_gb" type="number" min="0.01" step="0.01" required />
              </div>
              <div class="grid gap-2">
                <Label>AccessKey ID</Label>
                <Input v-model="form.ak" type="password" autocomplete="off"
                  :placeholder="instanceIndex !== null && management?.instances.find((i: any) => i.index === instanceIndex)?.access_key_configured ? '已保存，留空不修改' : '請輸入 AccessKey ID'" />
              </div>
              <div class="grid gap-2">
                <Label>AccessKey Secret</Label>
                <Input v-model="form.sk" type="password" autocomplete="off"
                  :placeholder="instanceIndex !== null && management?.instances.find((i: any) => i.index === instanceIndex)?.secret_key_configured ? '已保存，留空不修改' : '請輸入 AccessKey Secret'" />
              </div>
            </div>
            <div class="flex flex-wrap gap-6">
              <label class="flex items-center gap-2 text-sm">
                <Switch v-model:checked="form.actions_enabled" />
                允許自動開關機
              </label>
              <label class="flex items-center gap-2 text-sm">
                <Switch v-model:checked="form.instance_log_enabled" />
                啟用實例獨立日誌
              </label>
            </div>

            <Separator />

            <!-- 賬單 -->
            <label class="flex items-center gap-2 text-sm font-medium">
              <Switch v-model:checked="form.billing_enabled" @update:checked="form.billing_enabled = $event" />
              啟用賬單查詢
            </label>
            <div v-if="form.billing_enabled" class="grid gap-4">
              <div class="grid gap-2 sm:max-w-xs">
                <Label>賬單站點</Label>
                <Select v-model="form.billing_site">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="china">阿里雲中國站</SelectItem>
                    <SelectItem value="international">阿里雲國際站</SelectItem>
                    <SelectItem value="custom">自定義</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div v-if="form.billing_site === 'custom'" class="grid gap-4 sm:grid-cols-2">
                <div class="grid gap-2">
                  <Label>賬單 Endpoint</Label>
                  <Input v-model="form.billing_endpoint" placeholder="business.aliyuncs.com" />
                </div>
                <div class="grid gap-2">
                  <Label>賬單 Region</Label>
                  <Input v-model="form.billing_region" placeholder="cn-hongkong" />
                </div>
                <div class="grid gap-2">
                  <Label>貨幣代碼</Label>
                  <Input v-model="form.billing_currency_code" placeholder="USD" />
                </div>
                <div class="grid gap-2">
                  <Label>貨幣符號</Label>
                  <Input v-model="form.billing_currency_symbol" placeholder="$" />
                </div>
              </div>
            </div>

            <Separator />

            <!-- 排程 -->
            <label class="flex items-center gap-2 text-sm font-medium">
              <Switch v-model:checked="form.schedule_enabled" />
              啟用定時開關機
            </label>
            <div v-if="form.schedule_enabled" class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>開機時間</Label>
                <Input v-model="form.schedule_start" type="time" />
              </div>
              <div class="grid gap-2">
                <Label>關機時間</Label>
                <Input v-model="form.schedule_stop" type="time" />
              </div>
              <div class="grid gap-2 sm:col-span-2">
                <Label>排程時區</Label>
                <Select v-model="form.schedule_tz">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="tz in scheduleTimezones" :key="tz" :value="tz">{{ tz }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <p v-if="formError" class="text-sm text-destructive" role="alert">{{ formError }}</p>
            <DialogFooter>
              <Button type="button" variant="outline" @click="instanceDialogOpen = false">取消</Button>
              <Button type="submit">
                <Icon name="i-lucide-save" class="size-4" />
                保存
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <!-- 定時計劃對話框 -->
      <Dialog v-model:open="scheduleDialogOpen">
        <DialogContent class="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>定時開關機</DialogTitle>
          </DialogHeader>
          <form class="grid gap-4" @submit.prevent="saveSchedule">
            <label class="flex items-center gap-2 text-sm">
              <Switch v-model:checked="scheduleForm.enabled" />
              啟用定時開關機
            </label>
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>開機時間</Label>
                <Input v-model="scheduleForm.start" type="time" />
              </div>
              <div class="grid gap-2">
                <Label>關機時間</Label>
                <Input v-model="scheduleForm.stop" type="time" />
              </div>
              <div class="grid gap-2 sm:col-span-2">
                <Label>排程時區</Label>
                <Select v-model="scheduleForm.tz">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="tz in scheduleTimezones" :key="tz" :value="tz">{{ tz }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" @click="scheduleDialogOpen = false">取消</Button>
              <Button type="submit">保存計劃</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <!-- 自動發現對話框 -->
      <Dialog v-model:open="discoveryDialogOpen">
        <DialogContent class="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>自動發現阿里雲 ECS</DialogTitle>
          </DialogHeader>
          <div class="grid gap-4">
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>AccessKey ID</Label>
                <Input v-model="discovery.ak" type="password" autocomplete="off" required />
              </div>
              <div class="grid gap-2">
                <Label>AccessKey Secret</Label>
                <Input v-model="discovery.sk" type="password" autocomplete="off" required />
              </div>
              <div class="grid gap-2 sm:col-span-2">
                <Label>Region ID（逗號分隔）</Label>
                <Input v-model="discovery.regions" placeholder="留空掃描內置 Region" />
              </div>
              <div class="grid gap-2">
                <Label>標籤鍵</Label>
                <Input v-model="discovery.tag_key" placeholder="可留空" />
              </div>
              <div class="grid gap-2">
                <Label>標籤值</Label>
                <Input v-model="discovery.tag_value" placeholder="可留空" />
              </div>
              <div class="grid gap-2">
                <Label>統一關機閾值（GB）</Label>
                <Input v-model="discovery.traffic_limit_gb" type="number" min="0.01" step="0.01" required />
              </div>
              <div class="grid gap-2">
                <Label>賬單站點</Label>
                <Select v-model="discovery.billing_site">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="china">阿里雲中國站</SelectItem>
                    <SelectItem value="international">阿里雲國際站</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <label class="flex items-center gap-2 text-sm">
              <Switch v-model:checked="discovery.actions_enabled" />
              允許導入的實例自動開關機
            </label>

            <p v-if="discoveryMsg" class="text-sm" :class="discoveryMsgError ? 'text-destructive' : 'text-muted-foreground'">
              {{ discoveryMsg }}
            </p>

            <div class="grid gap-2">
              <label
                v-for="(item, index) in discovered"
                :key="index"
                class="flex items-center gap-3 rounded-lg border p-3 text-sm"
                :class="item.already_configured ? 'opacity-50' : ''"
              >
                <Checkbox
                  :model-value="selected.has(index)"
                  :disabled="item.already_configured"
                  @update:model-value="(v: boolean) => (v ? selected.add(index) : selected.delete(index))"
                />
                <div class="min-w-0">
                  <div class="font-medium">{{ item.name }} · {{ item.status }}</div>
                  <div class="truncate text-xs text-muted-foreground">
                    {{ item.region }} · {{ item.instance_id }}{{ item.public_ip ? ` · ${item.public_ip}` : '' }}{{ item.already_configured ? ' · 已配置' : '' }}
                  </div>
                </div>
              </label>
              <div v-if="!discovered.length" class="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                沒有發現符合條件的 ECS
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="discoveryDialogOpen = false">取消</Button>
            <Button variant="secondary" :disabled="scanning" @click="scanInstances">
              <Icon :name="scanning ? 'i-lucide-loader-circle' : 'i-lucide-search'" :class="scanning ? 'animate-spin' : ''" class="size-4" />
              掃描實例
            </Button>
            <Button :disabled="importing || !discovered.some(it => !it.already_configured)" @click="importInstances">
              <Icon name="i-lucide-plus" class="size-4" />
              導入所選
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  </div>
</template>
