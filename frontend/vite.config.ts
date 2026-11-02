import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'
import { defineConfig } from 'vite'

/**
 * NineVoxel Launcher 前端构建配置。
 *
 * 说明：本前端来自 Gemini 设计稿（`gemini design new/`），技术栈为 React 19 + Tailwind 4。
 * 与 Wails v3 的集成点是 `base: './'`——桌面端由 Wails 内置资源服务器加载 index.html，
 * 资源必须使用相对路径，否则会指向 `wails://` 的根而不是 dist 目录。
 *
 * 已移除设计稿自带的 AI Studio 相关配置：
 * - `server.hmr` / `server.watch` 的 `DISABLE_HMR` 开关（属于 AI Studio 编辑器避让逻辑）；
 * - `@google/genai`、`express`、`dotenv`、`motion` 等未使用的依赖（见 package.json）。
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: './',
  resolve: {
    alias: {
      '@': path.resolve(__dirname, '.'),
    },
  },
})
