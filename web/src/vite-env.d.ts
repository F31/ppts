/// <reference types="vite/client" />

// 显式声明参与安全门控的构建期开关（A01/A02）：
// VITE_ALLOW_DEV_IDENTITY 仅在构建时显式设为 "true" 才在生产构建里开放开发身份，
// 其余情况（含未设置）一律关闭。
interface ImportMetaEnv {
  readonly VITE_ALLOW_DEV_IDENTITY?: string;
}
