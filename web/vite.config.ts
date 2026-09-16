import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  define: {
    // 允许容器化部署下仍使用开发身份登录（无需 OIDC）
    'import.meta.env.VITE_ALLOW_DEV_IDENTITY': JSON.stringify('true'),
  },
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