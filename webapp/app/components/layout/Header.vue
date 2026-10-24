<script setup lang="ts">
// 頂欄：側欄開合 + 頁面標題 + 主題切換
const titles: Record<string, string> = {
  '/': '總覽',
  '/telegram': 'Telegram',
  '/logs': '運行日誌',
  '/settings': '設定',
  '/system': '備份與系統',
}

const route = useRoute()
const title = computed(() => titles[route.path] || 'Aliyun Guard')
</script>

<template>
  <header class="sticky top-0 md:peer-data-[variant=inset]:top-2 z-10 h-(--header-height) flex items-center gap-4 border-b bg-background px-4 md:px-6 md:rounded-tl-xl md:rounded-tr-xl">
    <div class="w-full flex items-center gap-4 h-4">
      <SidebarTrigger />
      <Separator orientation="vertical" />
      <span class="text-sm font-medium">{{ title }}</span>
    </div>
    <div class="ml-auto flex items-center gap-2">
      <slot />
    </div>
  </header>
</template>

<style scoped>

</style>
