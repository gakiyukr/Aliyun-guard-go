<script setup>
const router = useRouter()
</script>

<template>
  <div class="h-svh">
    <div class="m-auto h-full w-full flex flex-col items-center justify-center gap-2">
      <h1 class="text-[7rem] font-bold leading-tight">
        404
      </h1>
      <span class="font-medium">頁面不存在</span>
      <p class="text-center text-muted-foreground">
        您造訪的頁面不存在或已被移除。
      </p>
      <div class="mt-6 flex gap-4">
        <Button variant="outline" @click="router.back()">
          返回
        </Button>
        <Button @click="router.push('/')">
          回總覽
        </Button>
      </div>
    </div>
  </div>
</template>

<style scoped>

</style>
