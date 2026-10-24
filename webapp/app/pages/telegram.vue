<script setup lang="ts">
// Telegram 設定頁：對應舊面板 telegram tab（身份/測試/控制設定）。
// 節點代理與訂閱模組已裁決放棄，不呈現。
import { toast } from 'vue-sonner'
import { api, errorMessage } from '~/composables/useApi'

const m = ref<any>(null)

async function load(notifyError = true) {
  try {
    m.value = await api('/api/management')
  } catch (e: any) {
    if (notifyError) toast.error(errorMessage(e))
  }
}
onMounted(() => load())

const tg = computed(() => m.value?.telegram || {})

const identity = reactive({
  bot_token: '',
  chat_id: '',
  timeout_seconds: 12,
  retries: 3,
  control_enabled: false,
  control_admin_ids: '',
})

watch(tg, (v) => {
  if (!v || identity._filled) return
  identity.chat_id = v.chat_id ?? ''
  identity.timeout_seconds = v.timeout_seconds ?? 12
  identity.retries = v.retries ?? 3
  identity.control_enabled = v.control_enabled ?? false
  identity.control_admin_ids = (v.control_admin_ids || []).join(',')
  identity._filled = true
}, { immediate: true, deep: true })

const tokenPlaceholder = computed(() =>
  tg.value.bot_token_configured ? '已保存，留空不修改' : '尚未保存，請輸入 Bot Token')

async function saveIdentity() {
  try {
    await api('/api/telegram/identity', {
      method: 'POST',
      body: {
        bot_token: identity.bot_token.trim(),
        chat_id: String(identity.chat_id).trim(),
        timeout_seconds: Number(identity.timeout_seconds),
        retries: Number(identity.retries),
        control_enabled: identity.control_enabled,
        control_admin_ids: identity.control_admin_ids.trim(),
      },
    })
    identity.bot_token = ''
    toast.success('Telegram 機器人配置已保存')
    await load(false)
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}

const testing = ref(false)
async function testTelegram() {
  testing.value = true
  try {
    const data = await api<any>('/api/telegram/test', { method: 'POST', body: {} })
    const r = data.result
    const text = `${r.ok ? '測試訊息已發送' : '測試失敗'}${r.username ? `：@${r.username}` : ''}${r.error ? `：${r.error}` : ''}`
    if (r.ok) toast.success(text)
    else toast.error(text)
  } catch (e: any) {
    toast.error(errorMessage(e))
  } finally {
    testing.value = false
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
      <!-- 身份 -->
      <Card>
        <CardHeader>
          <CardTitle>Bot 身份與推送</CardTitle>
          <CardDescription>
            當前：{{ tg.connection_description }} · Bot 控制{{ tg.control_enabled ? '已開啟' : '已關閉' }}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form class="grid gap-4" @submit.prevent="saveIdentity">
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label>Bot Token</Label>
                <Input v-model="identity.bot_token" type="password" autocomplete="off" :placeholder="tokenPlaceholder" />
              </div>
              <div class="grid gap-2">
                <Label>Chat ID</Label>
                <Input v-model="identity.chat_id" />
              </div>
              <div class="grid gap-2">
                <Label>超時（秒）</Label>
                <Input v-model="identity.timeout_seconds" type="number" min="1" />
              </div>
              <div class="grid gap-2">
                <Label>重試次數</Label>
                <Input v-model="identity.retries" type="number" min="1" />
              </div>
            </div>

            <Separator />

            <label class="flex items-center gap-2 text-sm font-medium">
              <Switch v-model:checked="identity.control_enabled" />
              啟用 Bot 控制（私聊管理員指令）
            </label>
            <div class="grid gap-2">
              <Label>管理員 ID 列表（逗號分隔）</Label>
              <Input v-model="identity.control_admin_ids" placeholder="例如 12345678" />
              <p class="text-xs text-muted-foreground">
                {{ (tg.control_effective_admin_ids || []).length
                  ? `當前授權：${tg.control_effective_admin_ids.join(', ')}${tg.control_uses_chat_id ? '（使用 Chat ID）' : ''}`
                  : '當前沒有有效管理員，Bot 不會接受控制命令' }}
              </p>
            </div>

            <div class="flex gap-2">
              <Button type="submit">保存設定</Button>
              <Button type="button" variant="outline" :disabled="testing" @click="testTelegram">
                <Icon :name="testing ? 'i-lucide-loader-circle' : 'i-lucide-send'" :class="testing ? 'animate-spin' : ''" class="size-4" />
                發送測試訊息
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
