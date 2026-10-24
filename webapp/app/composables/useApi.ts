// API 客戶端：session cookie 同源自動攜帶；非 GET 請求補 X-CSRF-Token；
// 401 時導回登入頁。錯誤訊息展示後端 error 欄位原文。
// 注意：SPA 純客戶端執行，用模組級 ref（useState 若置於模組頂層會在
// import 時脫離 Nuxt 實例，觸發 "[nuxt] instance unavailable" 白屏）。
export const csrfToken = ref('')

export interface ApiError extends Error {
  status?: number
}

export async function api<T = any>(
  path: string,
  opts: { method?: string; body?: any; query?: Record<string, any> } = {},
): Promise<T> {
  const method = opts.method || 'GET'
  const headers: Record<string, string> = {}
  if (method !== 'GET' && csrfToken.value) {
    headers['X-CSRF-Token'] = csrfToken.value
  }
  try {
    return await $fetch<T>(path, {
      method,
      body: method === 'GET' ? undefined : opts.body,
      query: opts.query,
      headers,
    })
  } catch (e: any) {
    const status = e?.status ?? e?.statusCode ?? e?.response?.status
    if (status === 401 && path !== '/api/session' && path !== '/api/login') {
      navigateTo('/login')
    }
    const err = new Error(errorMessage(e)) as ApiError
    err.status = status
    throw err
  }
}

// errorMessage 取後端 {"ok":false,"error":"..."} 的 error 原文。
export function errorMessage(e: any): string {
  const data = e?.data ?? e?.response?._data
  if (data && typeof data === 'object' && typeof data.error === 'string' && data.error) {
    return data.error
  }
  if (typeof e?.message === 'string' && e.message) {
    return e.message
  }
  return '請求失敗'
}

// pollJob 輪詢 /api/job 直到 running=false（對應舊面板立即檢測後輪詢）。
export async function pollJob(
  onTick?: (job: any) => void,
  intervalMs = 1200,
  maxMs = 15 * 60 * 1000,
): Promise<any> {
  const start = Date.now()
  for (;;) {
    const job = await api<any>('/api/job')
    onTick?.(job)
    if (!job?.running) {
      return job
    }
    if (Date.now() - start > maxMs) {
      return job
    }
    await new Promise(resolve => setTimeout(resolve, intervalMs))
  }
}
