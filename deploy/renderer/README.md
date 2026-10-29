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

上下文已经在 `.dockerignore` 里收到约 6MB（`dist/`、`build/`、根目录 go build 产物都排除了），
所以从网络盘（WSL 的 `/mnt/e`、SMB 挂载）构建也不会卡在传上下文这一步。

## 构建期网络（构建机只能走代理时必看）

这个镜像在**构建期**有四处要出网，任一失败都会让 `docker build` 停在中途：

| 步骤 | 目标 | 失败表现 |
|---|---|---|
| `apt-get update/install` | Debian 源（curl/unzip/字体） | `Temporary failure resolving deb.debian.org`；单个包 `502 Bad Gateway` |
| `curl` Chrome | `storage.googleapis.com`，121MB | `curl: (56)` / `curl: (18) HTTP/2 stream not closed cleanly` / 超时 |
| `npm ci` | npm registry | `ECONNRESET` / `ETIMEDOUT`（表现为 `npm error network aborted`） |
| `go build` | `proxy.golang.org`（镜像内 `GOMODCACHE` 为空） | `dial tcp ... i/o timeout` |

代理要在 `docker build` 上传，不是 `docker run` 上——运行期镜像刻意无网：

```bash
docker build -f deploy/renderer/Dockerfile \
  --build-arg HTTP_PROXY=http://<proxy-host>:7890 \
  --build-arg HTTPS_PROXY=http://<proxy-host>:7890 \
  --build-arg NO_PROXY=storage.googleapis.com,registry.npmjs.org \
  --build-arg CHROME_VERSION="$(cat deploy/renderer/CHROME_VERSION)" \
  --build-arg CHROME_SHA256="$(cat deploy/renderer/chrome.sha256)" \
  -t ppts/renderer:dev .
```

`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 是 Docker 的预定义 build-arg，只在构建阶段作为环境变量
存在，不会写进镜像历史。

**`NO_PROXY` 里的两项不是可选项，是实测结论**。HTTP 代理对**大文件 / 长连接**不稳定：
- `storage.googleapis.com`（121MB）走代理会稳定在 60–80 秒左右被重置，
  `curl: (18)` 连试 6 次全挂；直连同一 URL 成功、126,938,225 字节、约 65 秒。
- `registry.npmjs.org` 走代理时 `curl` 直接 `Transferred a partial file`，
  `npm ci` 报 `ECONNRESET`；直连 200 / 10MB 包 1.1MB/s。

反过来 `deb.debian.org` 走代理更快，所以不要图省事把 `NO_PROXY` 设成 `*`。
Dockerfile 里 `curl` / `apt-get` / `npm ci` 各自带了重试（`--retry-all-errors`、
`Acquire::Retries`、`--fetch-retries`）：这几步要下几十到上百 MB，单次失败不该让整个构建重来。

**在 WSL2 里构建时，`<proxy-host>` 不一定是 127.0.0.1**：

- 默认 NAT 模式下，WSL 访问宿主的代理需要代理软件监听 `0.0.0.0`（Clash 的「允许局域网连接」），
  地址取 WSL 的默认网关：`ip route | awk '/^default/ {print $3}'`。
- Windows 11 22H2+ 的 mirrored 网络模式（`.wslconfig` 里 `networkingMode=mirrored`）下，
  `127.0.0.1` 可以直通宿主。
- 只监听 `127.0.0.1` 又没开 mirrored 时，容器里必然连不上代理，表现仍是 `curl: (56)`——
  这跟摘要、URL 都无关，别去改 `chrome.sha256`。

## 字体（`charset.txt`，改字集前必读）

镜像里的中文不是 `fonts-noto-cjk` 全量（4 个 TTC 共 91MB），而是构建期子集化的两个 OTF
（约 21.9MB）。边界写在 `deploy/renderer/charset.txt`，**那是唯一事实源**：

```bash
# 改完 charset.txt 直接构建即可，不需要手改任何版本号
docker build -f deploy/renderer/Dockerfile ... -t ppts/renderer:dev .
```

- `must` 行：每个可绘制码位都必须在产物里有字形，缺一个**构建失败**。汉字、中文标点、全角、
  假名、拉丁在这一档。
- 其余行：best-effort，字体有就收、没有就跳过（靠 Chrome 的 fallback 链，通常是 Liberation Sans）。
  希腊字母、箭头、制表符这类在 Noto CJK 里本来就只有部分字形，标 `must` 会让构建永远红。
- **不绘制但影响排版的码位照收**：空格 `U+0020`、全角空格 `U+3000`、不换行空格等属于 Zs，
  没有字形轮廓，但决定词间距与中英混排的断行位置。少了它们，用这份字体排出来的文字会在空格处
  变成方框（`5□段□sha256□`），或者整行宽度变化——断言不查它们，产出必须收它们。
- **不收 CJK Ext-A/Ext-B** 与 URO 末尾 `9FF0-9FFF`（Unicode 11 新增，本字体没收录）。
  命中这些字会是方框；要收就改 `charset.txt` 并把对应行标 `must`，构建会告诉你字体支不支持。

字体版本号由「实际保留码位集」的摘要推导（`2.004-sub03e7e8d7` 这种形式），构建期写进
`/etc/ppts/fonts.version`，由 `entrypoint.sh` 拼成 `-fonts` 交给服务，最终进 `planHash`。
摘要覆盖 `charset.txt` **和**保留集本身：只改 `subset-fonts.py` 的过滤逻辑而字集文件未动时，
版本也必须变，否则像素变了而缓存键没变。
**不要**在 `docker run` / compose 里手动传 `-fonts`：手工版本号迟早与 `charset.txt` 脱节，
而 `planHash` 恰恰靠它区分像素版本。

构建期的门禁（任一不过即失败）：`must` 码位覆盖率、子集后家族名仍是 `Noto Sans CJK SC`、
`fc-cache` 之后 `fc-list` 能精确匹配到该家族名。最后一条挡的是「装了但 fontconfig 认不出来」——
那种状态下 `/healthz` 会说字体会，Chrome 实际走 fallback。

`python3-fonttools` 只存在于构建期的 `fontsubset` 段，运行时镜像里没有 python。

## 自检（发布前必须跑）

```bash
docker run --rm --network none --read-only \
  --tmpfs /work:mode=1777 --tmpfs /tmp:mode=1777 \
  ppts/renderer:dev -selftest -backend remotion -workdir /work -renderer-script /app/render.mjs
```

成功输出 `selftest ok: backend=remotion pages=2 elapsed=...`，失败非零退出。
自检走的是与线上**同一个 Backend 实现**：staging（底板）→ Node → 片段校验，
所以「镜像能起、但渲染必挂」这种状态过不了门禁。

**两个 tmpfs 参数都不能省**（都是踩出来的）：

- `/work:mode=1777` —— 容器以 UID 10001 运行，而 `--tmpfs` 默认是 `root:root 0755`，
  会盖掉 Dockerfile 里的 `chown renderer`。漏了它报
  `mkdir /work/selftest-xxx: permission denied`。
- `/tmp:mode=1777` —— `--read-only` 下 `/tmp` 不可写，Remotion 的 bundler 要在那里
  `mkdtemp` 打包产物，漏了它报 `EROFS: read-only file system, mkdtemp '/tmp/remotion-webpack-bundle-xxx'`。

`stub` backend 会被明确拒绝——它写的是文本占位片段，自检它等于什么都没证明。

## 容量与资源限额（5.6 压测实测，改之前先读）

`compose.yml` 里的 `cpus / mem_limit / pids_limit` 与 `-concurrency` 是**互相咬合**的四个数，
不是独立旋钮。下表是 30 页 / 300s / 9000 帧、1080p30、细纹理底图的实测（完整用例见
`openspec/changes/animation-render-service-image/design.md` ADR-16）：

| 场景 | 核 | 内存上限 | 墙钟 | 峰值内存 | PIDs 峰值 | 结果 |
|---|---|---|---|---|---|---|
| 1 任务，300s 片 | 2 | 4g | 943s | 2666MB | 389 | 通过 |
| 1 任务，300s 片 | 2 | 2g | 21s | — | — | **OOM**（第一页） |
| 2 任务，各 300s 片 | 4 | 6g | 948s | 4826MB | 783 | 通过（60 段） |
| 2 任务，各 300s 片 | 2 | 4g | 56s | — | 482 | **OOM**（两个任务都死） |
| 1 任务，50s 片 | 2 | 4g，pids 256 | 139s | 2216MB | 257 | **失败**（第 4 页 EPIPE） |

三条必须记住的：

1. **`-concurrency` 有两个身份**：它同时是「同时跑几个任务」和「每个任务并行渲几帧」，
   满载时是 `concurrency²` 个并发渲染位。所以内存成本是乘积，不是加法。
2. **`pids_limit` 是这次最容易踩的**：撞上它的表现不是清晰报错，而是 Chrome 的 clone 失败后
   Node 报 `write EPIPE`、页面渲染到一半崩掉。PIDs 还随**页数**增长（5 页 264 → 30 页 389），
   不能只按并发数算。
3. **`/work` 挂 tmpfs 时产物占的是内存**：2×30 页写出 527MB，和 Chrome 抢同一个 `mem_limit`。
   片子更长就要给内存加余量，或把 `/work` 换成有容量上限的卷。

`-render-timeout` 不必手调：它现在按片长推导（`6 × 片长 + 5min`，夹在给定值与 6h 之间），
常量 15 分钟装不下 5 分钟长片（实测 943s）。

## 底图从哪来（`-assets-dir`，必读）

计划里的 `baseImage` 是**相对路径**，合成器用 Remotion 的 `staticFile()` 取图，而
`staticFile()` 的根就是 `plan.json` 所在目录——也就是本 job 的工作目录。
问题在于起跑前那个目录会被 `RemoveAll` 清空（重试语义要求上一轮半成品消失），
于是**没有资产根时，任何带底图的计划都会在 staging 阶段失败**：

```
准备底图失败: page 0 baseImage "p000.png": 资产不存在: stat /work/<job-id>/p000.png: no such file or directory
```

所以调用方必须把页面 PNG 放进一个只读目录，并用 `-assets-dir` 指给服务：

```bash
docker run --rm --network none --read-only \
  --tmpfs /work:mode=1777 --tmpfs /tmp:mode=1777 \
  -v /srv/ppts/pages:/assets:ro \
  ppts/renderer:dev -backend remotion -workdir /work -renderer-script /app/render.mjs \
  -assets-dir /assets
```

语义（`internal/render/assets.go`）：

- `baseImage` 同时相对**资产根**（读）与**工作目录**（写）解析，两侧都过同一套
  `SafeAssetPath` 校验；`..`、绝对路径、盘符一律拒绝。
- **只复制计划真正引用的文件**，按相对路径去重；不整目录搬运（工作目录常是 tmpfs）。
- 复制不改计划字节，因此 `planHash` 不受影响：同一份计划不会因为换了资产根而变成另一份缓存。
- 只接受普通文件：符号链接可以指向资产根之外，直接拒绝。
- 资产根在启动时校验存在且是目录；配错了服务起不来，而不是让每个任务各失败一次。
- 不传 `-assets-dir` 时保持旧行为（不搬任何东西）——带底图的计划会如上失败，
  这条路径只对「计划里没有任何元素级图层」的场景可用。

产物目录因此是**自包含**的：底图副本、`plates/`、`layers/`、`plan.json`、`segment_*.mp4`
都在同一个目录里，`JobTTL` 到期只删这一个目录。

## 计划诊断（`plan.warnings`，出片前先扫一眼）

诊断不阻断渲染，但意味着「成片和输入不完全一致」。全部码定义在 `internal/render/ir.go`。

| code | 含义 | 怎么处理 |
|---|---|---|
| `sibling_overlap_partial` | 两个动画元素**互不包含但部分重叠**（重叠 ≥ 较小的那块 5%）。ADR-9 的抹除只对包含关系成立，这种情形两张层位图会互相烘进对方的像素，动谁另一个就跟着拖影（ADR-20） | **修不掉**（单张栅格推不出逐元素 alpha）。要么改版式把两块拆开，要么接受；告警里带第一对元素 id |
| `duration_tier_conflict` | 有 N 条 clip 时长不在视觉基线的元素速度分层内（catalog 区间优先，未改动时长） | 通常是预设的固有偏差，可忽略；大量出现说明预设或 catalog 需要调 |
| `anchor_missing` / `box_clamped` / `clip_truncated` / `segment_unresolved` / `geometry_inferred` / `transition_defaulted` | 锚点/几何/裁剪/分段/画布/转场回退 | 逐条看 message：几何被 clamp 或 clip 被截断通常会直接反映在画面上 |

`sibling_overlap_partial` 的 5% 门槛是刻意的：文本框包围盒普遍带内边距，标题盒与副标题盒
常有一两像素交叠且交叠区没有墨，纯几何判定会天天误报，告警也就没人看了。

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
