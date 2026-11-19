import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { writeFileSync } from 'node:fs'

export default defineConfig({
  plugins: [
    react(),
    // 保留 dist/.gitkeep，保证未构建前端时 Go 也能编译
    { name: 'keep-dist', closeBundle() { writeFileSync('dist/.gitkeep', '') } },
  ],
  server: {
    proxy: { '/api': 'http://127.0.0.1:8080', '/files': 'http://127.0.0.1:8080', '/v1': 'http://127.0.0.1:8080', '/mcp': 'http://127.0.0.1:8080' },
  },
  build: { outDir: 'dist', assetsDir: 'static', emptyOutDir: true, chunkSizeWarningLimit: 900 },
})
