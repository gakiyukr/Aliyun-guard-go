<script setup lang="ts">
// 側欄底部：登出（呼叫後端清除 session）
const { isMobile, setOpenMobile } = useSidebar()
const loggingOut = ref(false)

async function handleLogout() {
  if (loggingOut.value) return
  loggingOut.value = true
  try {
    await api('/api/logout', { method: 'POST', body: {} })
  } catch {
    // 後端 session 已失效率視為登出成功
  }
  csrfToken.value = ''
  if (isMobile.value) setOpenMobile(false)
  navigateTo('/login')
}
</script>

<template>
  <SidebarMenu>
    <SidebarMenuItem>
      <SidebarMenuButton @click="handleLogout">
        <Icon name="i-lucide-log-out" />
        <span>登出</span>
      </SidebarMenuButton>
    </SidebarMenuItem>
  </SidebarMenu>
</template>

<style scoped>

</style>
