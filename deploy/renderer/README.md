# 渲染服务镜像

M2 的 `cmd/renderer` 只是 HTTP 壳，真实像素由 `renderer/`（Remotion）产出。这个目录负责把两者装进同一个镜像：
Go 进程对外提供 `/v1/render`，内部 exec Node 合成器、Chromium 逐帧渲染。

选型理由与能力核对见 `openspec/changes/animation-render-service-image/design.md`（ADR-1）。
一句话：**L0 页转场用 ffmpeg，不进这个镜像；L1 元素入场用 Remotion，因为 ffmpeg 没有干净的逐帧缩放路径。**

## 构建

```bash
# 1. 固定 Chrome（只做一次；换版本时重跑）
./deploy/renderer/pin-chrome.sh

# 2. 构建
docker build -f deploy/renderer/Dockerfile \
  --build-arg CHROME_VERSION="$(cat deploy/renderer/CHROME_VERSION)" \
  --build-arg CHROME_SHA256="$(cat deploy/renderer/chrome.sha256)" \
  -t ppts/renderer:dev .
```

`chrome.sha256` 由 `pin-chrome.sh` 生成，**必须提交**。它挡住的是「下载被截断/被镜像源替换」——
这类问题在运行时的表现是「渲染超时」，跟真实原因毫无关系。

## 自检（发布前必须跑）

```bash
docker run --rm --network none --read-only --tmpfs /work \
  ppts/renderer:dev -selftest -backend remotion -workdir /work -renderer-script /app/render.mjs
```

成功输出 `selftest ok: backend=remotion pages=2 elapsed=...`，失败非零退出。
自检走的是与线上**同一个 Backend 实现**：staging（底板）→ Node → 片段校验，
所以「镜像能起、但渲染必挂」这种状态过不了门禁。

`stub` backend 会被明确拒绝——它写的是文本占位片段，自检它等于什么都没证明。

## 版本

`/healthz` 返回：

```json
{"version":"remotion@4.0.539|chrome@156.0.8078.73","chromeVersion":"Google Chrome 156.0.8078.73","schemaVersion":"compiled-timeline/v1", ...}
```

- `version` 进 `planHash`：换 Chrome 或换 Remotion 都会让缓存失效并重渲（否则升级后会拿到旧像素）。
- `schemaVersion` 不符才是错误：客户端会拒绝调用。滚动升级期间新旧实例 `version` 不同不阻断。

发版时把 `-version` 传成上面的格式，不要留 `dev`。

## 已知阻塞（镜像首次构建前必须解决）

1. **`go mod download` 在干净环境会失败**：`github.com/F31/textnorm@v0.2.2` 的已发布内容与
   `go.sum` 记录的摘要不一致（`checksum mismatch`）。本机靠 `go.work` 指向 `./textnorm` 绕过，
   镜像里 `GOWORK=off` 没有这条退路。需要模块所有者确认正确的版本/摘要后更新 `go.sum`，
   本变更不擅自改它。
2. **`chrome.sha256` 尚未生成**：本机代理下载 Chrome for Testing 失败（curl 56）。
   在有外网的构建机上跑一次 `pin-chrome.sh` 并提交产物即可。
