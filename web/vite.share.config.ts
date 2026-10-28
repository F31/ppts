import { defineConfig } from 'vite';

// 分享成品播放器的独立构建（M6-1）。
//
// 与站内 SPA 构建完全分离：
//   - lib 模式 + IIFE：产物是经典脚本，`file://` 下用 <script src> 可直接加载
//     （ES module / fetch 在 file:// 下会被 CORS 拦住，"双击能播"就没了）。
//   - 产物直接落到 internal/share/assets/，由 go:embed 打进二进制，因此 `go build`
//     不需要 Node；CI 通过 `git diff --exit-code internal/share/assets/` 防止漂移。
//   - emptyOutDir 必须为 false：该目录同时放着 assets.go（embed 声明），清空会把源码删掉。
export default defineConfig({
  build: {
    outDir: '../internal/share/assets',
    emptyOutDir: false,
    target: 'es2020',
    cssCodeSplit: false,
    lib: {
      entry: 'src/sharePlayer/main.ts',
      name: 'PptsSharePlayer',
      formats: ['iife'],
      fileName: () => 'app.js',
      cssFileName: 'app'
    }
  }
});
