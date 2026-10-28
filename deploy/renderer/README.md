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

## 重启与工作目录

服务是无状态的：`jobs` 只在内存，重启后上一轮的所有 job id 立即作废（拿旧 id 查询返回 404，
与「从未存在」不可区分）。因此 `NewService` 启动时会**清扫 `-workdir` 下的孤儿目录**，
避免容器反复重启把磁盘塞满（ADR-13）。

- 只删名字匹配 `ShortID`（24 位小写十六进制，可带 `-rN` 重试后缀）的目录；
  `selftest-*`、`lost+found`、运维放的其它东西都不会被碰。
- 清扫失败（句柄未释放、权限不足）不阻塞启动，记日志后由下次重启重试。
- 成功时打印一行：`render: 启动清扫删除 3 个孤儿目录，释放 128.4 MiB`。

**多副本共用一个 `-workdir` 时必须加 `-keep-orphans`**：否则先起来的副本会把其他副本
正在写的目录删掉，表现为偶发产物丢失。共享存储的正确形态是每个副本一个子目录，
并且各自只清扫自己的目录。

## 已知阻塞（两条均已解决，保留记录以免重踩）

1. ~~**`go mod download` 在干净环境会失败**~~ —— **2026-10-28 已修**。原因是 `go.sum` 里
   `textnorm@v0.2.2` 的摘要写错了（记 `h1:d3rO7+…`，而 `sum.golang.org` 与真实下载都是
   `h1:IZDOaanu…`；v0.2.0 / v0.2.1 同样错，说明这些摘要从未对着发布包验证过）。已按 sumdb 值改正，
   `GOWORK=off go build/test ./...` 已跑通。构建镜像前仍建议先跑一次
   `GOWORK=off go mod verify` 确认摘要基线。
2. ~~**`chrome.sha256` 尚未生成**（本机代理下载 Chrome 失败，curl 56）~~ —— **2026-10-28 已生成并提交**。
   代理 `127.0.0.1:7890` 下可正常下载（1 分 12 秒 / 121 MiB）。`156.0.8078.73` 的摘要为
   `77eb8d52798efc7a857ab61abcc62a456d8ea9635b02341d06dffee2761768c8`，与 GCS 的 `x-goog-hash: md5=`
   及 `Content-Length` 一致，zip 内 287 项含 `chrome-headless-shell-linux64/chrome-headless-shell`。
   重跑 `pin-chrome.sh` 会重新下载并复算，产出一致可直接 diff。

   注意 `chrome.sha256` 必须保持 **LF**（`.gitattributes` 已固定 `text eol=lf`）：它是 `sha256sum -c`
   的输入，CRLF 会让校验行变成非法格式。另：Git Bash 下旧版脚本会用 `sha256sum "$path"` 写出
   `\<hex>`（coreutils 对含反斜杠路径的转义标记），已改为从 stdin 读并断言 64 位十六进制。
