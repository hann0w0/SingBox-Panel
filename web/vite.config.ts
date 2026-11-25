import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// Dev server proxies /api requests to the Go panel (default http://127.0.0.1:32334).
// In production the built assets in dist/ are served by the panel itself.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5173,
    proxy: {
      '/api': process.env.SINGBOX_PANEL_DEV_API ?? 'http://127.0.0.1:32334',
    },
  },
  build: {
    outDir: 'dist',
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (
            id.includes('/node_modules/react/') ||
            id.includes('/node_modules/react-dom/') ||
            id.includes('/node_modules/scheduler/') ||
            id.includes('/node_modules/react-router/') ||
            id.includes('/node_modules/react-router-dom/')
          ) {
            return 'vendor-react'
          }
          if (
            id.includes('/node_modules/radix-ui/') ||
            id.includes('/node_modules/@radix-ui/') ||
            id.includes('/node_modules/cmdk/') ||
            id.includes('/node_modules/@floating-ui/')
          ) {
            return 'vendor-ui'
          }
          if (id.includes('/node_modules/lucide-react/')) {
            return 'vendor-icons'
          }
          if (id.includes('/node_modules/qrcode.react/')) {
            return 'vendor-qr'
          }
          return undefined
        },
      },
    },
  },
})
