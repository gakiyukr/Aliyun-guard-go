<script setup lang="ts">
import { Toaster } from '@/components/ui/sonner'
import 'vue-sonner/style.css'

const colorMode = useColorMode()
const color = computed(() => colorMode.value === 'dark' ? '#09090b' : '#ffffff')

useHead({
  meta: [
    { charset: 'utf-8' },
    { name: 'viewport', content: 'width=device-width, initial-scale=1' },
    { key: 'theme-color', name: 'theme-color', content: color },
  ],
  link: [
    { rel: 'icon', href: '/favicon.ico' },
  ],
  htmlAttrs: {
    lang: 'zh-CN',
  },
})

useSeoMeta({
  title: 'Aliyun Guard 控制面板',
  description: '阿里雲 ECS 保活與 CDT 流量守護控制面板',
})
</script>

<template>
  <Body class="overscroll-none antialiased bg-background text-foreground">
    <div id="app" class="relative">
      <NuxtLayout>
        <NuxtPage />
      </NuxtLayout>
      <ConfirmHost />
    </div>

    <Toaster :theme="colorMode.preference as any || 'system'" :duration="4200" :max="4" />
  </Body>
</template>
