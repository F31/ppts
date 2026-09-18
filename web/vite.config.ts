import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  // 注意：不要在这里 define VITE_ALLOW_DEV_IDENTITY —— 发行版登录页不允许出现开发身份入口。
  // 开发态（vite dev）由 auth.ts 的 import.meta.env.DEV 自动放行；确有联调需要时用
  // `VITE_ALLOW_DEV_IDENTITY=true npm run build` 显式构建。
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/ppts.v1.': {
        target: 'http://localhost:8080',
        changeOrigin: true
      },
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true
      },
      '/ppts/object': {
        target: 'http://localhost:8080',
        changeOrigin: true
      },
      '/healthz': {
        target: 'http://localhost:8080',
        changeOrigin: true
      },
      '/debug/vars': {
        target: 'http://localhost:8080',
        changeOrigin: true
      }
    }
  }
});