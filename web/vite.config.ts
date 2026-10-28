import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import ui from '@nuxt/ui/vite'

export default defineConfig({
  plugins: [vue(), ui({
    colorMode: false,
    icon: { mode: 'svg', clientBundle: { scan: true } },
    ui: { colors: { primary: 'emerald', neutral: 'slate' } },
  })],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },
  },
  preview: { host: '127.0.0.1', port: 4173, strictPort: true },
  build: { target: ['chrome111', 'safari16.4', 'firefox128'] },
})
