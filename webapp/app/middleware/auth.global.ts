// 全域認證守衛：應用啟動時探測一次 /api/session；未登入導向 /login。
// 之後的 401 由 api() 統一攔截導向。
export default defineNuxtRouteMiddleware(async (to) => {
  if (to.path === '/login') {
    return
  }
  const checked = useState<boolean>('ag-auth-checked', () => false)
  if (checked.value) {
    return
  }
  checked.value = true
  try {
    const session = await $fetch<any>('/api/session')
    if (session?.authenticated) {
      csrfToken.value = session.csrf || ''
      return
    }
  } catch {
    // 服務不可達時也導向登入頁
  }
  return navigateTo('/login', { replace: true })
})
