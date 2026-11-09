/// <reference types="vitest/config" />
import { fileURLToPath } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// 前后端分离开发：前端跑 5173，/api 代理到本地 Go 服务 8080。
// 这样浏览器里不存在跨域，也不需要后端放宽 CORS。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // 路径别名必须三处一致：这里、tsconfig.json 的 paths、components.json 的 aliases。
    // 只改一处会出现"编辑器能跳转、构建报模块找不到"的不对称错误。
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
  test: {
    // jsdom 提供 document/window，Testing Library 才有 DOM 可渲染。
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    // 组件里 import 的 CSS 由 vite 处理；测试里保留为空操作即可
    css: false,
    restoreMocks: true,
  },
})
