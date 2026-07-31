import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev server proxies API + subscription paths to the Go panel on :8080.
// In production the built assets in dist/ are served by the panel itself.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:32334',
    },
  },
  build: {
    outDir: 'dist',
  },
})
