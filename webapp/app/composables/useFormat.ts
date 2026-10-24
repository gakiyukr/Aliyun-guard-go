// 格式化工具：與舊面板 JS 逐字對齊（fmtNum/fmtDate/statusInfo）
export function fmtNum(value: any, digits = 2): string {
  if (value === null || value === undefined) return '--'
  return Number(value).toFixed(digits)
}

export function fmtDate(value: any): string {
  if (!value) return '尚未運行'
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

// statusInfo 回傳 {label, kind}，kind: 'running'|'stopped'|'transition'|'unknown'
export function statusInfo(status: string, paused: boolean): { label: string, kind: string } {
  if (paused) return { label: '已暫停', kind: 'unknown' }
  if (status === 'Running') return { label: '運行中', kind: 'running' }
  if (status === 'Stopped') return { label: '已停止', kind: 'stopped' }
  if (['Starting', 'Stopping', 'Pending'].includes(status)) return { label: status, kind: 'transition' }
  return { label: status || 'Unknown', kind: 'unknown' }
}
