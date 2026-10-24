<script setup lang="ts">
// 登入頁：用戶名 + 密碼，錯誤訊息展示後端原文（含限流 429）
definePageMeta({
  layout: 'blank',
})

const username = ref('')
const password = ref('')
const showPassword = ref(false)
const error = ref('')
const loading = ref(false)

async function submit() {
  if (loading.value) return
  error.value = ''
  loading.value = true
  try {
    const res = await api<any>('/api/login', {
      method: 'POST',
      body: { username: username.value, password: password.value },
    })
    csrfToken.value = res.csrf || ''
    navigateTo('/')
  } catch (e: any) {
    error.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="relative flex items-center justify-center min-h-dvh px-4">
    <Card class="w-full max-w-sm">
      <CardHeader class="text-center">
        <div class="mx-auto mb-2 flex size-12 items-center justify-center rounded-xl bg-primary text-primary-foreground">
          <Icon name="i-lucide-activity" class="size-6" />
        </div>
        <CardTitle class="text-2xl">
          登入控制台
        </CardTitle>
        <CardDescription>受保護的運維入口</CardDescription>
      </CardHeader>
      <CardContent>
        <form class="grid gap-4" autocomplete="on" @submit.prevent="submit">
          <div class="grid gap-2">
            <Label for="username">用戶名</Label>
            <Input id="username" v-model="username" name="username" autocomplete="username" required />
          </div>
          <div class="grid gap-2">
            <Label for="password">密碼</Label>
            <div class="relative">
              <Input
                id="password"
                v-model="password"
                name="password"
                :type="showPassword ? 'text' : 'password'"
                autocomplete="current-password"
                required
                class="pr-10"
              />
              <button
                type="button"
                class="absolute inset-y-0 right-0 flex items-center px-3 text-muted-foreground hover:text-foreground"
                title="顯示或隱藏密碼"
                @click="showPassword = !showPassword"
              >
                <Icon :name="showPassword ? 'i-lucide-eye-off' : 'i-lucide-eye'" class="size-4" />
              </button>
            </div>
          </div>
          <p v-if="error" class="text-sm text-destructive" role="alert">{{ error }}</p>
          <Button type="submit" class="w-full" :disabled="loading">
            <Icon name="i-lucide-log-in" class="size-4" />
            登入
          </Button>
        </form>
      </CardContent>
      <CardFooter>
        <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Icon name="i-lucide-shield-check" class="size-3.5" />
          流量保活 · 自動止損 · 賬單通知
        </p>
      </CardFooter>
    </Card>
  </div>
</template>
