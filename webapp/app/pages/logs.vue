<script setup lang="ts">
// 運行日誌頁：系統總日誌 / 實例獨立日誌切換、行數上限、自動刷新
import { toast } from 'vue-sonner'
import { api, errorMessage } from '~/composables/useApi'

const route = useRoute()
const management = ref<any>(null)
const source = ref('system')
const limit = ref(200)
const logs = ref<any>(null)
const autoRefresh = ref(true)
const loading = ref(false)

const instances = computed(() => management.value?.instances || [])
const currentInstance = computed(() =>
  instances.value.find((i: any) => String(i.index) === source.value))

async function load(notifyError = true) {
  if (loading.value) return
  loading.value = true
  try {
    const query: Record<string, any> = { limit: limit.value }
    if (source.value !== 'system') query.instance = source.value
    logs.value = await api('/api/logs', { query })
  } catch (e: any) {
    if (notifyError) toast.error(errorMessage(e))
  } finally {
    loading.value = false
  }
}

watch(source, () => load(false))

let timer: ReturnType<typeof setInterval> | null = null
onMounted(async () => {
  try {
    management.value = await api('/api/management')
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
  // 支援從實例卡片「查看獨立日誌」跳轉
  if (route.query.instance) {
    source.value = String(route.query.instance)
  }
  load(false)
  timer = setInterval(() => {
    if (!document.hidden && autoRefresh.value) load(false)
  }, 15000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

async function toggleInstanceLog() {
  const inst = currentInstance.value
  if (!inst || !inst.toggle_available) return
  try {
    await api(`/api/instances/${inst.index}/logging`, {
      method: 'POST',
      body: { enabled: !inst.instance_log_enabled },
    })
    toast.success(!inst.instance_log_enabled ? '實例日誌已啟用' : '實例日誌已關閉')
    await load(false)
    management.value = await api('/api/management')
  } catch (e: any) {
    toast.error(errorMessage(e))
  }
}
</script>

<template>
  <div class="grid gap-4">
    <div class="flex flex-wrap items-end gap-3">
      <div class="grid gap-2 sm:w-80">
        <Label>日誌來源</Label>
        <Select v-model="source">
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="system">系統總日誌</SelectItem>
            <SelectItem
              v-for="item in instances"
              :key="item.index"
              :value="String(item.index)"
            >
              {{ item.name }} ({{ item.instance_id }}) · {{ item.instance_log_enabled ? '已啟用' : '已關閉' }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="grid gap-2 sm:w-32">
        <Label>行數</Label>
        <Select v-model="limit">
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="n in [50, 100, 200, 500]" :key="n" :value="n">{{ n }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <label class="flex items-center gap-2 pb-2 text-sm">
        <Switch v-model:checked="autoRefresh" />
        自動刷新
      </label>
      <div class="ml-auto flex items-center gap-2 pb-1">
        <Button v-if="source !== 'system' && currentInstance?.toggle_available" variant="outline" size="sm" @click="toggleInstanceLog">
          <Icon name="i-lucide-toggle-left" class="size-4" />
          {{ currentInstance.instance_log_enabled ? '關閉獨立日誌' : '啟用獨立日誌' }}
        </Button>
        <Button variant="outline" size="sm" @click="load(false)">
          <Icon name="i-lucide-refresh-cw" class="size-4" />
          刷新
        </Button>
      </div>
    </div>

    <Card class="py-4">
      <CardContent class="px-4">
        <pre
          v-if="logs?.lines?.length"
          class="max-h-[65dvh] overflow-auto rounded-lg bg-muted p-4 font-mono text-xs leading-relaxed"
        >{{ logs.lines.join('\n') }}</pre>
        <div v-else class="py-16 text-center text-sm text-muted-foreground">
          <Icon name="i-lucide-scroll-text" class="mx-auto mb-2 size-8" />
          <p>暫無日誌內容</p>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
