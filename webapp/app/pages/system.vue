<script setup lang="ts">
// 備份與系統頁：對應舊面板 system tab——運行狀態、本機加密備份建立/還原、
// S3 自動備份設定/測試/立即備份/清單/還原、服務重啟。
// 程式更新與回滾快照已裁決放棄，不呈現。
import { toast } from 'vue-sonner'
import { api, errorMessage } from '~/composables/useApi'
import { confirmAction } from '~/composables/useConfirm'
import { fmtDate } from '~/composables/useFormat'

const m = ref<any>(null)
const dashboard = ref<any>(null)

async function load(notifyError = true) {
  try {
    const [mg, d] = await Promise.all([api('/api/management'), api('/api/dashboard')])
    m.value = mg
    dashboard.value = d
  } catch (e: any) {
    if (notifyError) toast.error(errorMessage(e))
  }
}
onMounted(() => load())

// --- 本機加密備份 ---
const backupPassword = ref('')
const backupState = ref(true)
const backupLogs = ref(true)
const creating = ref(false)

function downloadBase64(filename: string, encoded: string) {
  const bytes = Uint8Array.from(atob(encoded), char => char.charCodeAt(0))
  const url = URL.createObjectURL(new Blob([bytes], { type: 'application/json' }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

async function createBackup() {
  creating.value = true
  try {
    const data = await api<any>('/api/backup/create', {
      method: 'POST',
      body: { password: backupPassword.value, include_state: backupState.value, include_logs: backupLogs.value },
    })
    downloadBase64(data.result.filename, data.result.backup_base64)
    backupPassword.value = ''
    toast.success(`加密備份已創建：${data.result.filename}`)
  } catch (e: any) {
    toast.error(errorMessage(e))
  } finally {
    creating.value = false
  }
}

// --- 備份檔還原 ---
const restoreFile = ref<File | null>(null)
const restorePassword = ref('')
const restoreLogs = ref(true)
const restorePreview = ref('')
const restorePreviewError = ref(false)
const restoreReady = ref(false)
const restoreBase64 = ref('')
const previewing = ref(false)
const restoring = ref(false)

function fileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('無法讀取備份文件'))
    reader.onload = () => resolve(String(reader.result).split(',', 2)[1] || '')
    reader.readAsDataURL(file)
  })
}

watch([restoreFile, restorePassword], () => {
  restoreReady.value = false
  restorePreview.value = ''
})

async function previewRestore() {
  if (!restoreFile.value) {
    toast.error('請選擇備份文件')
    return
  }
  previewing.value = true
  try {
    restoreBase64.value = await fileAsBase64(restoreFile.value)
    const data = await api<any>('/api/backup/preview', {
      method: 'POST',
      body: { backup_base64: restoreBase64.value, password: restorePassword.value },
    })
    const changed = data.result.files.filter((item: any) => item.action !== 'unchanged')
    restorePreview.value = `備份包含 ${data.result.summary.instances || 0} 個實例、${data.result.summary.nodes || 0} 個節點；${changed.length} 個文件將新增或替換：${changed.map((item: any) => item.path).join('、') || '無變化'}`
    restorePreviewError.value = false
    restoreReady.value = true
  } catch (e: any) {
    restoreReady.value = false
    restorePreview.value = errorMessage(e)
    restorePreviewError.value = true
  } finally {
    previewing.value = false
  }
}

async function doRestore() {
  if (!restoreReady.value || !restoreBase64.value) return
  const ok = await confirmAction({
    title: '恢復備份',
    message: '確認按預覽結果恢復？當前配置會先自動創建安全備份。',
    confirmText: '確認恢復',
    tone: 'warning',
  })
  if (!ok) return
  restoring.value = true
  try {
    await api('/api/backup/restore', {
      method: 'POST',
      body: { backup_base64: restoreBase64.value, password: restorePassword.value, include_logs: restoreLogs.value },
    })
    toast.success('備份已恢復，後臺服務即將重啟')
    restoreReady.value = false
  } catch (e: any) {
    toast.error(errorMessage(e))
  } finally {
    restoring.value = false
  }
}

// --- S3 自動備份 ---
const s3 = reactive({
  enabled: false, bucket: '', region: '', endpoint_url: '', prefix: '',
  addressing_style: 'auto', use_iam_role: false,
  access_key_id: '', secret_access_key: '', session_token: '', clear_session_token: false,
  backup_password: '',
  schedule: 'daily', time: '03:00', weekday: 1,
  retention: 30, include_state: true, include_logs: true,
  notification_mode: 'errors', server_side_encryption: 'AES256', kms_key_id: '',
})
const s3Msg = ref('')
const s3MsgError = ref(false)
const s3Busy = ref('')

watch(() => m.value?.s3_backup, (v) => {
  if (!v || s3._filled) return
  s3.enabled = v.enabled
  s3.bucket = v.bucket || ''
  s3.region = v.region || ''
  s3.endpoint_url = v.endpoint_url || ''
  s3.prefix = v.prefix || ''
  s3.addressing_style = v.addressing_style || 'auto'
  s3.use_iam_role = v.uses_iam_role
  s3.schedule = v.schedule || 'daily'
  s3.time = v.time || '03:00'
  s3.weekday = Number(v.weekday ?? 1)
  s3.retention = Number(v.retention ?? 30)
  s3.include_state = v.include_state !== false
  s3.include_logs = v.include_logs !== false
  s3.notification_mode = v.notification_mode || 'errors'
  s3.server_side_encryption = v.server_side_encryption || 'AES256'
  s3.kms_key_id = v.kms_key_id || ''
  s3._filled = true
}, { immediate: true, deep: true })

const s3Status = computed(() =>
  m.value?.s3_backup?.enabled
    ? `已啟用 · 最近成功 ${fmtDate(m.value?.s3_backup?.status?.last_success_at)}`
    : '已關閉')

function s3SettingsPayload() {
  return {
    enabled: s3.enabled,
    bucket: s3.bucket.trim(),
    region: s3.region.trim(),
    endpoint_url: s3.endpoint_url.trim(),
    prefix: s3.prefix.trim(),
    addressing_style: s3.addressing_style,
    use_iam_role: s3.use_iam_role,
    access_key_id: s3.access_key_id.trim(),
    secret_access_key: s3.secret_access_key.trim(),
    session_token: s3.session_token.trim(),
    clear_session_token: s3.clear_session_token,
    backup_password: s3.backup_password,
    schedule: s3.schedule,
    time: s3.time,
    weekday: Number(s3.weekday),
    retention: Number(s3.retention),
    include_state: s3.include_state,
    include_logs: s3.include_logs,
    notification_mode: s3.notification_mode,
    server_side_encryption: s3.server_side_encryption,
    kms_key_id: s3.kms_key_id.trim(),
  }
}

async function saveS3Settings() {
  s3Busy.value = 'save'
  s3Msg.value = ''
  try {
    await api('/api/s3-backup/settings', { method: 'POST', body: s3SettingsPayload() })
    toast.success('S3 自動備份設定已保存')
    s3.backup_password = ''
    s3.access_key_id = ''
    s3.secret_access_key = ''
    s3.session_token = ''
    await load(false)
  } catch (e: any) {
    s3Msg.value = errorMessage(e)
    s3MsgError.value = true
  } finally {
    s3Busy.value = ''
  }
}

async function testS3() {
  s3Busy.value = 'test'
  s3Msg.value = '正在測試 S3 連接...'
  s3MsgError.value = false
  try {
    const data = await api<any>('/api/s3-backup/test', { method: 'POST', body: s3SettingsPayload() })
    s3Msg.value = `連接成功：${data.result.bucket} · ${data.result.endpoint} · ${data.result.latency_ms} ms`
  } catch (e: any) {
    s3Msg.value = errorMessage(e)
    s3MsgError.value = true
  } finally {
    s3Busy.value = ''
  }
}

async function runS3Now() {
  const ok = await confirmAction({
    title: '立即備份',
    message: '立即使用已保存的 S3 設定創建並上傳一份加密備份？',
    confirmText: '立即備份',
  })
  if (!ok) return
  s3Busy.value = 'run'
  s3Msg.value = '正在創建加密備份並上傳...'
  s3MsgError.value = false
  try {
    const data = await api<any>('/api/s3-backup/run', { method: 'POST', body: {} })
    s3Msg.value = `上傳成功：s3://${data.result.bucket}/${data.result.key}；清理 ${data.result.deleted.length} 份舊備份`
    await loadS3Backups()
  } catch (e: any) {
    s3Msg.value = errorMessage(e)
    s3MsgError.value = true
  } finally {
    s3Busy.value = ''
  }
}

// --- S3 備份清單 ---
const s3Backups = ref<any[]>([])
const s3RestoreKey = ref('')
const s3RestoreReady = ref(false)

async function loadS3Backups() {
  s3Busy.value = 'list'
  s3Msg.value = '正在讀取雲端備份...'
  s3MsgError.value = false
  try {
    const data = await api<any>('/api/s3-backup/list')
    s3Backups.value = data.backups
    s3Msg.value = `已讀取 ${data.backups.length} 份雲端備份`
  } catch (e: any) {
    s3Msg.value = errorMessage(e)
    s3MsgError.value = true
  } finally {
    s3Busy.value = ''
  }
}

async function previewS3(key: string) {
  s3Busy.value = `preview:${key}`
  s3Msg.value = '正在下載並驗證雲端備份...'
  s3MsgError.value = false
  try {
    const data = await api<any>('/api/s3-backup/preview', { method: 'POST', body: { key } })
    const changed = data.result.files.filter((item: any) => item.action !== 'unchanged')
    s3RestoreKey.value = key
    s3RestoreReady.value = true
    s3Msg.value = `已預覽 ${key.split('/').pop()}：${data.result.summary.instances || 0} 個實例，${changed.length} 個文件將新增或替換：${changed.map((item: any) => item.path).join('、') || '無變化'}`
  } catch (e: any) {
    s3RestoreReady.value = false
    s3RestoreKey.value = ''
    s3Msg.value = errorMessage(e)
    s3MsgError.value = true
  } finally {
    s3Busy.value = ''
  }
}

async function restoreS3(key: string) {
  if (!s3RestoreReady.value || s3RestoreKey.value !== key) {
    s3Msg.value = '請先預覽這份雲端備份，再執行恢復。'
    s3MsgError.value = true
    return
  }
  const ok = await confirmAction({
    title: '恢復雲端備份',
    message: '確認按剛纔的差異預覽恢復這份雲端備份？當前配置會先自動創建安全備份。',
    confirmText: '確認恢復',
    tone: 'warning',
  })
  if (!ok) return
  s3Busy.value = `restore:${key}`
  try {
    await api('/api/s3-backup/restore', { method: 'POST', body: { key, include_logs: true } })
    s3RestoreReady.value = false
    s3RestoreKey.value = ''
    toast.success('雲端備份已恢復，後臺服務即將重啟')
  } catch (e: any) {
    s3Msg.value = errorMessage(e)
    s3MsgError.value = true
  } finally {
    s3Busy.value = ''
  }
}

// --- 服務重啟 ---
async function restartService() {
  const ok = await confirmAction({
    title: '重啟後臺服務',
    message: '確認重啟後臺服務？網頁可能短暫斷開。',
    confirmText: '重啟',
    tone: 'warning',
  })
  if (!ok) return
  try {
    await api('/api/service/restart', { method: 'POST', body: {} })
    toast.success('後臺服務重啟已安排')
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}
</script>

<template>
  <div class="grid max-w-4xl gap-4">
    <div v-if="!m" class="flex items-center justify-center py-20 text-muted-foreground">
      <Icon name="i-lucide-loader-circle" class="mr-2 size-5 animate-spin" />
      載入中
    </div>
    <template v-else>
      <!-- 運行狀態 -->
      <Card>
        <CardHeader>
          <CardTitle>運行狀態</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-2 text-sm sm:grid-cols-2">
          <div>後端：<span class="font-medium">{{ m.backend }}</span></div>
          <div>版本：<span class="font-medium">v{{ m.version }}</span></div>
          <div>本機地址：<span class="font-medium">{{ m.local_ip || '未檢測到' }}</span></div>
          <div>瀏覽器訪問：<span class="font-medium">{{ m.browser_url }}</span></div>
          <div class="sm:col-span-2">累計檢測輪數：<span class="font-medium">{{ dashboard?.service?.cycle_count ?? '--' }}</span></div>
          <div class="sm:col-span-2 pt-2">
            <Button variant="outline" @click="restartService">
              <Icon name="i-lucide-refresh-cw" class="size-4" />
              重啟後臺服務
            </Button>
          </div>
        </CardContent>
      </Card>

      <!-- 本機加密備份 -->
      <Card>
        <CardHeader>
          <CardTitle>本機加密備份</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-4">
          <form class="grid gap-4" @submit.prevent="createBackup">
            <div class="grid gap-4 sm:grid-cols-3">
              <div class="grid gap-2">
                <Label>備份密碼</Label>
                <Input v-model="backupPassword" type="password" required />
              </div>
              <label class="flex items-center gap-2 self-end text-sm">
                <Checkbox :model-value="backupState" @update:model-value="(v: boolean) => backupState = v" />
                包含 state.json
              </label>
              <label class="flex items-center gap-2 self-end text-sm">
                <Checkbox :model-value="backupLogs" @update:model-value="(v: boolean) => backupLogs = v" />
                包含日誌
              </label>
            </div>
            <div>
              <Button type="submit" :disabled="creating">
                <Icon :name="creating ? 'i-lucide-loader-circle' : 'i-lucide-download'" :class="creating ? 'animate-spin' : ''" class="size-4" />
                創建並下載備份
              </Button>
            </div>
          </form>

          <Separator />

          <div class="grid gap-3">
            <div class="text-sm font-medium">從備份文件恢復</div>
            <div class="grid gap-3 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>備份文件（.agbackup）</Label>
                <input
                  type="file"
                  accept=".agbackup"
                  class="file:mr-3 file:rounded-md file:border-0 file:bg-secondary file:px-3 file:py-1.5 file:text-sm text-sm"
                  @change="(e: any) => restoreFile = e.target?.files?.[0] || null"
                >
              </div>
              <div class="grid gap-2">
                <Label>備份密碼</Label>
                <Input v-model="restorePassword" type="password" />
              </div>
            </div>
            <p v-if="restorePreview" class="text-sm" :class="restorePreviewError ? 'text-destructive' : 'text-muted-foreground'">
              {{ restorePreview }}
            </p>
            <div class="flex flex-wrap gap-2">
              <Button variant="outline" :disabled="previewing || !restoreFile" @click="previewRestore">
                <Icon :name="previewing ? 'i-lucide-loader-circle' : 'i-lucide-search'" :class="previewing ? 'animate-spin' : ''" class="size-4" />
                預覽
              </Button>
              <Button variant="destructive" :disabled="!restoreReady || restoring" @click="doRestore">
                <Icon name="i-lucide-refresh-cw" class="size-4" />
                恢復
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- S3 自動備份 -->
      <Card>
        <CardHeader>
          <CardTitle>S3 自動備份</CardTitle>
          <CardDescription>{{ s3Status }}</CardDescription>
        </CardHeader>
        <CardContent class="grid gap-4">
          <form class="grid gap-4" @submit.prevent="saveS3Settings">
            <label class="flex items-center gap-2 text-sm">
              <Switch v-model:checked="s3.enabled" />
              啟用 S3 自動備份
            </label>
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>Bucket</Label>
                <Input v-model="s3.bucket" />
              </div>
              <div class="grid gap-2">
                <Label>Region</Label>
                <Input v-model="s3.region" placeholder="ap-northeast-1" />
              </div>
              <div class="grid gap-2">
                <Label>Endpoint（可留空使用 AWS）</Label>
                <Input v-model="s3.endpoint_url" placeholder="https://..." />
              </div>
              <div class="grid gap-2">
                <Label>對象前綴</Label>
                <Input v-model="s3.prefix" placeholder="aliyun-guard/" />
              </div>
              <div class="grid gap-2">
                <Label>尋址方式</Label>
                <Select v-model="s3.addressing_style">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">auto</SelectItem>
                    <SelectItem value="virtual-host">virtual-host</SelectItem>
                    <SelectItem value="path">path</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="grid gap-2">
                <Label>加密方式</Label>
                <Select v-model="s3.server_side_encryption">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="AES256">AES256</SelectItem>
                    <SelectItem value="aws:kms">aws:kms</SelectItem>
                    <SelectItem value="">不指定</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div v-if="s3.server_side_encryption === 'aws:kms'" class="grid gap-2 sm:col-span-2">
                <Label>KMS Key ID</Label>
                <Input v-model="s3.kms_key_id" />
              </div>
            </div>

            <label class="flex items-center gap-2 text-sm">
              <Switch v-model:checked="s3.use_iam_role" />
              使用 IAM Role（實例/環境憑據）
            </label>
            <div v-if="!s3.use_iam_role" class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>Access Key ID</Label>
                <Input v-model="s3.access_key_id" type="password" autocomplete="off"
                  :placeholder="m?.s3_backup?.access_key_configured ? '已保存，留空不修改' : '請輸入 Access Key ID'" />
              </div>
              <div class="grid gap-2">
                <Label>Secret Access Key</Label>
                <Input v-model="s3.secret_access_key" type="password" autocomplete="off"
                  :placeholder="m?.s3_backup?.secret_key_configured ? '已保存，留空不修改' : '請輸入 Secret Access Key'" />
              </div>
              <div class="grid gap-2">
                <Label>Session Token（可選）</Label>
                <Input v-model="s3.session_token" type="password" autocomplete="off"
                  :placeholder="m?.s3_backup?.session_token_configured ? '已保存，留空不修改' : ''" />
              </div>
              <label class="flex items-center gap-2 self-end text-sm">
                <Checkbox v-model:checked="s3.clear_session_token" />
                清除已保存的 Session Token
              </label>
            </div>

            <div class="grid gap-2">
              <Label>備份加密密碼</Label>
              <Input v-model="s3.backup_password" type="password"
                :placeholder="m?.s3_backup?.password_configured ? '已保存，留空不修改' : '用於生成 .agbackup 加密備份'" />
            </div>

            <div class="grid gap-4 sm:grid-cols-4">
              <div class="grid gap-2">
                <Label>週期</Label>
                <Select v-model="s3.schedule">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="hourly">每小時</SelectItem>
                    <SelectItem value="daily">每天</SelectItem>
                    <SelectItem value="weekly">每週</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="grid gap-2">
                <Label>時間</Label>
                <Input v-model="s3.time" type="time" :disabled="s3.schedule === 'hourly'" />
              </div>
              <div v-if="s3.schedule === 'weekly'" class="grid gap-2">
                <Label>星期</Label>
                <Select v-model="s3.weekday">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="(name, i) in ['週一', '週二', '週三', '週四', '週五', '週六', '週日']" :key="i" :value="i">
                      {{ name }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="grid gap-2">
                <Label>保留份數</Label>
                <Input v-model="s3.retention" type="number" min="1" max="365" />
              </div>
            </div>

            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>通知模式</Label>
                <Select v-model="s3.notification_mode">
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="always">always（每次都通知）</SelectItem>
                    <SelectItem value="errors">errors（僅失敗通知）</SelectItem>
                    <SelectItem value="none">none（不通知）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="flex flex-wrap items-end gap-6 pb-1 text-sm">
                <label class="flex items-center gap-2">
                  <Checkbox v-model:checked="s3.include_state" />
                  包含 state.json
                </label>
                <label class="flex items-center gap-2">
                  <Checkbox v-model:checked="s3.include_logs" />
                  包含日誌
                </label>
              </div>
            </div>

            <p v-if="s3Msg" class="text-sm" :class="s3MsgError ? 'text-destructive' : 'text-muted-foreground'">
              {{ s3Msg }}
            </p>

            <div class="flex flex-wrap gap-2">
              <Button type="submit" :disabled="!!s3Busy">保存設定</Button>
              <Button type="button" variant="outline" :disabled="!!s3Busy" @click="testS3">測試連接</Button>
              <Button type="button" variant="outline" :disabled="!!s3Busy" @click="runS3Now">立即備份</Button>
              <Button type="button" variant="ghost" :disabled="!!s3Busy" @click="loadS3Backups">讀取雲端備份</Button>
            </div>
          </form>

          <Separator />

          <div class="grid gap-2">
            <div v-for="item in s3Backups" :key="item.key" class="flex items-center justify-between gap-3 rounded-lg border p-3">
              <div class="min-w-0">
                <div class="truncate text-sm font-medium">{{ item.name }}</div>
                <div class="text-xs text-muted-foreground">{{ (item.size / 1048576).toFixed(2) }} MiB · {{ fmtDate(item.modified_at) }}</div>
              </div>
              <div class="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" :disabled="!!s3Busy" @click="previewS3(item.key)">預覽</Button>
                <Button variant="destructive" size="sm" :disabled="!!s3Busy" @click="restoreS3(item.key)">恢復</Button>
              </div>
            </div>
            <div v-if="!s3Backups.length" class="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
              雲端沒有 Aliyun Guard 加密備份
            </div>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
