import { writeFileSync } from 'node:fs';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  // 注意：不要在这里 define VITE_ALLOW_DEV_IDENTITY —— 发行版登录页不允许出现开发身份入口。
  // 开发态（vite dev）由 auth.ts 的 import.meta.env.DEV 自动放行；确有联调需要时用
  // `VITE_ALLOW_DEV_IDENTITY=true npm run build` 显式构建。
  plugins: [
    react(),
    {
      // dist/ 里必须始终存在至少一个文件：Go 侧 web/embed.go 用 `//go:embed all:dist`，
      // 目录不存在或为空时 `go build ./...` / `go vet ./...` 直接报
      // "pattern all:dist: no matching files found"。vite build 会清空 outDir，
      // 于是每次构建都会把占位文件删掉，一次 `git add -A` 就会把删除提交上去——
      // 历史上删过四次，CI 的 lint job 因此长期红着（race/test/postgres 等一律被 skip）。
      // 构建结束把占位写回去，删不掉就不会再复发。
      name: 'keep-dist-placeholder',
      closeBundle() {
        // 用 import.meta.url 定位，避免 cwd 不同（npm 在 web/ 下跑，CI 也一样）时写错地方。
        writeFileSync(new URL('./dist/.gitkeep', import.meta.url), '');
      }
    }
  ],
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