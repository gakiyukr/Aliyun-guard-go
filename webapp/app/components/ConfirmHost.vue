<script setup lang="ts">
// 全域確認框宿主（掛在 app.vue）
import { confirmState, closeConfirm } from '~/composables/useConfirm'
</script>

<template>
  <AlertDialog v-model:open="confirmState.open">
    <AlertDialogContent @escape-key-down.prevent @interact-outside.prevent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ confirmState.options.title }}</AlertDialogTitle>
        <AlertDialogDescription>{{ confirmState.options.message }}</AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel @click="closeConfirm(false)">{{ confirmState.options.cancelText || '取消' }}</AlertDialogCancel>
        <AlertDialogAction
          :class="confirmState.options.tone === 'warning' ? 'bg-destructive text-white hover:bg-destructive/90' : ''"
          @click="closeConfirm(true)"
        >
          {{ confirmState.options.confirmText || '確認' }}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
