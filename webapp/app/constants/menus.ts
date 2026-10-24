import type { NavMenu } from '~/types/nav'

// 側欄導覽：與舊面板五個 tab 對應（dashboard/telegram/logs/settings/system）；
// 實例管理併入總覽（單頁形態與舊面板一致）。
export const navMenu: NavMenu[] = [
  {
    heading: '監控',
    items: [
      {
        title: '總覽',
        icon: 'i-lucide-layout-dashboard',
        link: '/',
      },
    ],
  },
  {
    heading: '通知與日誌',
    items: [
      {
        title: 'Telegram',
        icon: 'i-lucide-send',
        link: '/telegram',
      },
      {
        title: '運行日誌',
        icon: 'i-lucide-scroll-text',
        link: '/logs',
      },
    ],
  },
  {
    heading: '系統',
    items: [
      {
        title: '設定',
        icon: 'i-lucide-settings',
        link: '/settings',
      },
      {
        title: '備份與系統',
        icon: 'i-lucide-database-backup',
        link: '/system',
      },
    ],
  },
]
