import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// Dev server proxies API + subscription paths to the Go panel on :8080.
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
  },
})
