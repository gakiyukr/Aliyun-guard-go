<script setup lang="ts">
// 設定頁：全域設定 + Web 面板設定（對應舊面板 settings tab 兩個表單）
import { toast } from 'vue-sonner'
import { api, errorMessage } from '~/composables/useApi'
import { confirmAction } from '~/composables/useConfirm'

const m = ref<any>(null)

async function load(notifyError = true) {
  try {
    m.value = await api('/api/management')
  } catch (e: any) {
    if (notifyError) toast.error(errorMessage(e))
  }
}
onMounted(() => load())

// 全域設定
const s = reactive({
  interval_seconds: 300,
  billing_cache_seconds: 3600,
  notification_mode: 'always',
  force_ipv4: true,
  notify_on_daemon_start: true,
  start_wait_seconds: 8,
  stop_wait_seconds: 10,
  start_poll_seconds: 5,
  watchdog_enabled: false,
  watchdog_timeout_seconds: 300,
  watchdog_failure_threshold: 3,
})

watch(() => m.value?.settings, (v) => {
  if (!v || s._filled) return
  s.interval_seconds = v.interval_seconds
  s.billing_cache_seconds = v.billing_cache_seconds
  s.notification_mode = v.notification_mode
  s.force_ipv4 = v.force_ipv4
  s.notify_on_daemon_start = v.notify_on_daemon_start
  s.start_wait_seconds = v.start_wait_seconds
  s.stop_wait_seconds = v.stop_wait_seconds
  s.start_poll_seconds = v.start_poll_seconds
  s.watchdog_enabled = v.watchdog?.enabled ?? false
  s.watchdog_timeout_seconds = v.watchdog?.timeout_seconds ?? 300
  s.watchdog_failure_threshold = v.watchdog?.failure_threshold ?? 3
  s._filled = true
}, { immediate: true, deep: true })

async function saveSettings() {
  try {
    await api('/api/settings', {
      method: 'POST',
      body: {
        interval_seconds: Number(s.interval_seconds),
        billing_cache_seconds: Number(s.billing_cache_seconds),
        notification_mode: s.notification_mode,
        force_ipv4: s.force_ipv4,
        notify_on_daemon_start: s.notify_on_daemon_start,
        start_wait_seconds: Number(s.start_wait_seconds),
        stop_wait_seconds: Number(s.stop_wait_seconds),
        start_poll_seconds: Number(s.start_poll_seconds),
        watchdog: {
          enabled: s.watchdog_enabled,
          timeout_seconds: Number(s.watchdog_timeout_seconds),
          failure_threshold: Number(s.watchdog_failure_threshold),
        },
      },
    })
    toast.success('全局設定已保存')
    await load(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}

// Web 面板設定
const w = reactive({
  enabled: false, host: '127.0.0.1', port: 8765,
  username: '', password: '', password_confirm: '',
})

watch(() => m.value?.web, (v) => {
  if (!v || w._filled) return
  w.enabled = v.enabled
  w.host = v.host
  w.port = v.port
  w.username = v.username
  w._filled = true
}, { immediate: true, deep: true })

const webWarning = computed(() =>
  w.host === '0.0.0.0'
    ? '所有網卡監聽允許直接 HTTP 訪問，用戶名和密碼會以明文經過網路；建議配置防火牆或 HTTPS 反向代理。'
    : '僅本機監聽適合 SSH 隧道或 HTTPS 反向代理。')

async function saveWebSettings() {
  const changedToPublic = w.host === '0.0.0.0' && m.value?.web?.host !== '0.0.0.0'
  let confirmed = true
  if (changedToPublic) {
    confirmed = await confirmAction({
      title: '切換爲公網監聽',
      message: '所有網卡監聽下，直接 HTTP 訪問會明文傳輸登入密碼。確認繼續？',
      confirmText: '繼續保存',
      tone: 'warning',
    })
  }
  if (!confirmed) return
  try {
    const data = await api<any>('/api/web-settings', {
      method: 'POST',
      body: {
        enabled: w.enabled,
        host: w.host,
        port: Number(w.port),
        username: w.username.trim(),
        password: w.password,
        password_confirm: w.password_confirm,
        confirm_public: confirmed,
      },
    })
    toast.success(data.web?.restart_required ? '網頁配置已保存，後臺服務即將重啟' : '網頁配置已保存')
    w.password = ''
    w.password_confirm = ''
    await load(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}
</script>

<template>
  <div class="grid max-w-3xl gap-4">
    <div v-if="!m" class="flex items-center justify-center py-20 text-muted-foreground">
      <Icon name="i-lucide-loader-circle" class="mr-2 size-5 animate-spin" />
      載入中
    </div>
    <template v-else>
      <!-- 全域設定 -->
      <Card>
        <CardHeader>
          <CardTitle>全局設定</CardTitle>
        </CardHeader>
        <CardContent>
          <form class="grid gap-4" @submit.prevent="saveSettings">
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>檢測間隔（秒）</Label>
                <Input v-model="s.interval_seconds" type="number" min="60" />
              </div>
              <div class="grid gap-2">
                <Label>賬單緩存（秒）</Label>
                <Input v-model="s.billing_cache_seconds" type="number" min="300" />
              </div>
              <div class="grid gap-2">
                <Label>通知模式</Label>
                <Select v-model="s.notification_mode">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="always">always（每次檢測都通知）</SelectItem>
                    <SelectItem value="changes">changes（有變化才通知）</SelectItem>
                    <SelectItem value="errors">errors（僅異常通知）</SelectItem>
                    <SelectItem value="none">none（不通知）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="grid gap-2">
                <Label>開機等待（秒）</Label>
                <Input v-model="s.start_wait_seconds" type="number" min="0" />
              </div>
              <div class="grid gap-2">
                <Label>關機等待（秒）</Label>
                <Input v-model="s.stop_wait_seconds" type="number" min="0" />
              </div>
              <div class="grid gap-2">
                <Label>開機輪詢間隔（秒）</Label>
                <Input v-model="s.start_poll_seconds" type="number" min="1" />
              </div>
            </div>
            <div class="flex flex-wrap gap-6">
              <label class="flex items-center gap-2 text-sm">
                <Switch v-model:checked="s.force_ipv4" />
                強制 IPv4
              </label>
              <label class="flex items-center gap-2 text-sm">
                <Switch v-model:checked="s.notify_on_daemon_start" />
                守護啟動時發送通知
              </label>
            </div>
            <Separator />
            <div class="grid gap-4 sm:grid-cols-3">
              <div class="grid gap-2">
                <Label>看門狗</Label>
                <div class="flex items-center gap-2 text-sm">
                  <Switch v-model:checked="s.watchdog_enabled" />
                  {{ s.watchdog_enabled ? '已啟用' : '已關閉' }}
                </div>
              </div>
              <div class="grid gap-2">
                <Label>超時（秒）</Label>
                <Input v-model="s.watchdog_timeout_seconds" type="number" min="60" />
              </div>
              <div class="grid gap-2">
                <Label>失敗閾值</Label>
                <Input v-model="s.watchdog_failure_threshold" type="number" min="1" />
              </div>
            </div>
            <div>
              <Button type="submit">保存全局設定</Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <!-- Web 面板設定 -->
      <Card>
        <CardHeader>
          <CardTitle>Web 主控臺設定</CardTitle>
          <CardDescription :class="w.host === '0.0.0.0' ? 'text-amber-600 dark:text-amber-400' : ''">
            {{ webWarning }}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form class="grid gap-4" @submit.prevent="saveWebSettings">
            <label class="flex items-center gap-2 text-sm">
              <Switch v-model:checked="w.enabled" />
              啟用網頁面板
            </label>
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>監聽地址</Label>
                <Select v-model="w.host">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="127.0.0.1">127.0.0.1（僅本機）</SelectItem>
                    <SelectItem value="0.0.0.0">0.0.0.0（所有網卡）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="grid gap-2">
                <Label>端口</Label>
                <Input v-model="w.port" type="number" min="1024" max="65535" />
              </div>
              <div class="grid gap-2">
                <Label>登入用戶名</Label>
                <Input v-model="w.username" />
              </div>
              <div class="grid gap-2">
                <Label>新密碼（留空不修改）</Label>
                <Input v-model="w.password" type="password" autocomplete="new-password"
                  :placeholder="m?.web?.password_configured ? '已保存，留空不修改' : '至少 8 個字符'" />
              </div>
              <div class="grid gap-2">
                <Label>確認新密碼</Label>
                <Input v-model="w.password_confirm" type="password" autocomplete="new-password" />
              </div>
            </div>
            <div>
              <Button type="submit">保存網頁配置</Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
