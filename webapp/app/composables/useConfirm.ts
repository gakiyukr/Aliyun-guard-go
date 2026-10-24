// 全域確認框：Promise 化 confirm（對應舊面板 confirmAction）。
export interface ConfirmOptions {
  title: string
  message: string
  confirmText?: string
  cancelText?: string
  tone?: 'primary' | 'warning'
}

export const confirmState = reactive<{
  open: boolean
  options: ConfirmOptions
  resolve: ((ok: boolean) => void) | null
}>({
  open: false,
  options: { title: '', message: '' },
  resolve: null,
})

export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  return new Promise((resolve) => {
    confirmState.options = options
    confirmState.open = true
    confirmState.resolve = resolve
  })
}

// ConfirmHost 內部呼叫：關閉並回傳結果
export function closeConfirm(ok: boolean) {
  confirmState.resolve?.(ok)
  confirmState.open = false
  confirmState.resolve = null
}
