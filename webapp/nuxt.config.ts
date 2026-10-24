import tailwindcss from '@tailwindcss/vite';

// 阿里雲 Guard 面板前端：SPA 模式（ssr:false），nuxt generate 產出純靜態
// 檔案供 Go binary go:embed；開發時 /api 經 devProxy 轉發至本機 Go 服務。
export default defineNuxtConfig({
  ssr: false,
  devtools: { enabled: false },

  css: ['~/assets/css/tailwind.css'],
  vite: {
    plugins: [tailwindcss()]
  },

  components: [
    {
      path: '~/components',
      extensions: ['.vue']
    }
  ],

  modules: [
    'shadcn-nuxt',
    '@vueuse/nuxt',
    '@nuxt/icon',
    '@nuxtjs/color-mode',
    '@nuxt/fonts'
  ],

  shadcn: {
    /**
     * Prefix for all the imported component
     */
    prefix: '',
    /**
     * Directory that the component lives in.
     * @default "~/components/ui"
     */
    componentDir: '~/components/ui'
  },

  colorMode: {
    classSuffix: ''
  },

  fonts: {
    defaults: {
      weights: [300, 400, 500, 600, 700, 800]
    }
  },

  imports: {
    dirs: ['./lib']
  },

  compatibilityDate: '2026-03-13',

  // 開發代理：Go 面板服務（go run ./cmd/guard serve）監聽 8765
  devProxy: {
    '/api': {
      target: 'http://127.0.0.1:8765/api',
      changeOrigin: false
    }
  },

  typescript: {
    typeCheck: false
  },

  app: {
    baseURL: '/'
  }
})
