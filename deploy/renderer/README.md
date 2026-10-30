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

成功输出 `selftest ok: backend=remotion pages=2 elapsed=... degraded=0`，失败非零退出。
自检走的是与线上**同一个 Backend 实现**：staging（底板）→ Node → 片段校验，
所以「镜像能起、但渲染必挂」这种状态过不了门禁。

`degraded` 不是装饰：自检样例底图齐全，出现非零就说明链路有问题（例如底图取不到）。

### 验收单页降级（tasks 3.7）

真实渲染里没有可靠的「让某一页失败」的造法，所以故障是注入的：

```bash
# 让第 2 页（0 基）第一次渲染必然失败，期望：它被换成静态画面、片段仍然齐全
docker run --rm --network none --read-only \
  --tmpfs /work:mode=1777 --tmpfs /tmp:mode=1777 \
  ppts/renderer:dev -selftest -selftest-degrade-page 1 \
  -backend remotion -workdir /work -renderer-script /app/render.mjs
# → selftest ok: ... degraded=1
```

自检会断言「恰好降级那一页、原因是 `page_render_failed`」（`page_skipped` 表示静态重试
也失败、该页会变成空洞，直接算失败）。要拿成片做帧比对时加 `-selftest-keep`，
产物目录会打印在输出里。

判据不能只看「片段都在」：`renderMedia` 只换 `inputProps` 不换 `composition` 时，
静态重试实际重渲了原动画，任务照样成功、片段照样齐全。2026-10-29 的实测是
降级页第 8 帧与末帧差 **42926 像素**（差异框正好落在标题条 box 内），换成
`selectComposition` 重新计算后归零（mean 0.072、`>8` 像素 0）。

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

## 片段缓存与页级并行（M5）

三个新开关，都是可选的，不配就是引入缓存前的行为：

| 开关 | 默认 | 作用 |
|---|---|---|
| `-segment-cache <dir>` | 空（关闭） | 逐页片段的缓存目录。**必须在持久卷上**，不能放 `/work` / `/tmp`（tmpfs 重启即空） |
| `-segment-cache-max-bytes` | 4 GiB | 缓存容量软上限；超限按 mtime 从旧到新淘汰到 90% |
| `-page-concurrency` | 1（串行） | 同时渲染几页；**总渲染位 = 任务并发 × 页并发 × 帧并发** |

四条使用纪律：

1. **缓存目录必须共享才有意义**：单副本就是本地卷；多副本指向同一份共享存储，
   命中才能跨副本。缓存按 `root/<key[:2]>/<key>.mp4` 布局，键是内容的 SHA-256。
2. **它不会让渲染失败**：读错误按未命中处理，写错误只记日志。截断的缓存项会被
   最小容器校验拦下并重渲——缓存坏了的代价是「慢一次」，不是「导出失败」。
3. **被降级的页不写缓存**：降级是瞬时故障的表现，缓存它会让「这页永远不动」固化下来。
4. **页级并行按乘积算内存**：`-concurrency` 同时喂「任务数」与「帧并发」，
   所以 `-concurrency 2 -page-concurrency 2` 是 2×2×2 = 8 个位，不是 4 个。

### 达标配置（30s/1080p 的 P95 ≤ 120s）

同一台机器、10 页 × 3 秒 / 1080p30 / 5 次冷渲染（`cmd/render-perf -scenario cold`）：

| 配置 | 渲染位 | P95 | 结论 |
|---|---|---|---|
| `-concurrency 2`（页并发 1） | 2 | 133.9s | **不达标**（默认值，差 14 秒） |
| `-concurrency 2 -page-concurrency 2` | 8（2 任务） | 71.6s* | 达标（按 1 任务算 4 位） |
| **`-concurrency 1 -page-concurrency 4`** | **4** | **49.3s** | **达标且最快：推荐** |

\* 该行实测时只跑 1 个任务，所以实际是 4 位。

两条结论：

- **默认的 2 位达不到 120s 目标**。要达标必须显式开页级并行；推荐
  `-concurrency 1 -page-concurrency 4`（1 个任务 × 4 页 × 1 帧 = 4 位，正好对上 4 vCPU）。
- **同样是 4 个位，「4 页 × 1 帧」比「2 页 × 2 帧」快 30%**（49.3s vs 71.6s）。
  页级并行更省，因为每页一个标签页、图片解码与合成器往返不再互相排队。

观测：`GET /v1/render/{id}` 的 `cachedPages` / `renderedPages` 是本次的命中与实渲页数。
门禁命令（需要 Chrome + ffmpeg，几分钟）：

```bash
go run ./cmd/render-perf -pages 10 -seconds 3 -runs 5 \
  -chrome "$PPTS_CHROME" -json        # 冷渲染 P95 / 全命中二次渲染 / 命中率 / 三路一致性
go run ./cmd/render-perf -pages 10 -seconds 3 -scenario parallel -page-concurrency 3 \
  -chrome "$PPTS_CHROME"             # 页级并行：产物一致 + 串行对照墙钟
```

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

## 单机部署：导出侧怎么把页图放进去（tasks 2.0）

渲染服务只读自己的资产根，而页图在对象存储里，两者之间必须先有人把字节搬过去。
单机部署用**共享目录**，三个环境变量把链路接通：

| 变量 | 谁读 | 含义 |
|---|---|---|
| `PPTS_ANIMATION_ENABLED=true` | 导出 worker **和 api** | 总开关。不开就完全不建客户端、不探测 |
| `PPTS_RENDERER_URL` | 导出 worker **和 api** | 渲染服务地址，例如 `http://renderer:8090` |
| `PPTS_ANIMATION_ASSET_DIR` | 导出 worker **和 api** | **导出侧看到的**资产根目录（写）；同一个卷在渲染容器里挂成 `-assets-dir`（读） |

**api 与 worker 都要配三个。** 这不是冗余（api 侧第三项从 tasks 3.3 起是单页预览的硬依赖）：

- worker 少了它们 → 导出永远静默降级成静态版（用户勾了动画也拿不到）。
- api 少了它们 → 导出对话框里的「动画视频」永远置灰，reason 是 `renderer_not_configured`。
  功能其实是好的，只是没人能勾。
- api 少了 `PPTS_ANIMATION_ASSET_DIR` → 单页预览整块不可用（路由不注册）：预览要先把页图
  投递到渲染服务能读的位置，而"只配了地址没配目录"比"没开预览"更难解释——提交能成功，
  图永远找不到，用户看到的是"预览失败"而不是"这个部署没开预览"。

api 侧的探测**每次真实请求** `/healthz`（10s TTL 缓存），而不是读配置判断：
"配置里写了地址但容器没起来"是常态，按配置报可用等于骗用户点一个必然降级的选项。

```yaml
services:
  api:
    environment:
      PPTS_ANIMATION_ENABLED: "true"
      PPTS_RENDERER_URL: http://renderer:8090
      PPTS_ANIMATION_ASSET_DIR: /srv/ppts/pages
    volumes:
      - /srv/ppts/pages:/srv/ppts/pages

  worker:
    environment:
      PPTS_ANIMATION_ENABLED: "true"
      PPTS_RENDERER_URL: http://renderer:8090
      PPTS_ANIMATION_ASSET_DIR: /srv/ppts/pages
    volumes:
      - /srv/ppts/pages:/srv/ppts/pages

  renderer:
    volumes:
      - /srv/ppts/pages:/assets:ro
    command: ["-addr", ":8090", "-backend", "remotion", "-workdir", "/work",
              "-renderer-script", "/app/render.mjs", "-assets-dir", "/assets"]
```

导出侧与预览侧写入的路径长这样：`pages/<sha256 前 2 位>/<sha256>.png`。
两边用同一个实现（`app.LocalAssetPublisher`），同一份页图落在同一个文件上——预览不会
给导出增加额外的缓存条目。

单页预览（tasks 3.3）的预算与画质是刻意与导出不同的：960 宽 / 24fps / 3s 超时。
按导出参数（1080p30）渲一页在纯 CPU 上轻易超过 3s，那样每次编辑都要等超时提示；
预览要回答的是"第 4 段开始时这行字出现了吗"，不是像素校对。超时会 `DELETE` 掉渲染任务，
否则用户连点五次预览就在渲染服务里排了五个必然被丢掉的任务。

### 租户级开放与灰度（M4）

`PPTS_ANIMATION_ENABLED` 从 M4 起只是**部署闸**：它开着只代表这台机器有能力出动画，
具体租户能不能用由 `tenants.policy` 的 `animation` 段决定：

```json
{"animation": {"enabled": true, "track_editor": true, "preview": true, "max_minutes": 120}}
```

判定优先级：**部署闸 > 租户显式 > 租户灰度 > 部署默认**。

| 变量 | 默认 | 说明 |
|---|---|---|
| `PPTS_ANIMATION_ROLLOUT` | `100` | 租户没配过策略时的默认灰度百分比 |
| `PPTS_ANIMATION_ROLLOUT_SALT` | 空 | 分桶盐。**写进配置后不要再改**——改盐等于重新洗牌，会把已经吃到灰度的租户踢回未开放状态 |

**默认 100 是刻意的**：它等于"部署闸开 = 所有租户可用"，与引入租户策略之前完全一致。
直接改成 0 会让升级瞬间关掉所有客户的动画。生产收口按两步走：

1. 设 `PPTS_ANIMATION_ROLLOUT=0`，重启 api；
2. 给白名单租户写 `{"animation":{"enabled":true}}`，`enabled:true` 绕过灰度直接生效。

要按比例放量，给这些租户写 `rollout_percent`（或调大部署默认百分比）。租户被封禁写
`enabled:false`——它压过 100% 的灰度，不会在放量时被重新打开。

关闭后：写规格返回 404、导出选项返回 `animation_disabled_by_tenant`、新导出产出与静态版
逐字节相同的产物。**已在渲染的任务不受影响**，照常完成——钱已经花了，让在途任务失败没有收益。

#### 渲染分钟额度

`animation.max_minutes` 是**每个 UTC 月**的动画渲染分钟数上限。留空或写 0 = 不限量
（design ADR-7：升级当天不给存量租户加硬停，60/月是运营配置值，不是代码默认值）。

- **受理时预占**：按时间轴总时长占用额度，超限直接 `429 animation_quota_exceeded`，
  响应体带上限、已用、本次申请量与 `resetAt`，**不会建任务排队**。
- **只对真的渲了动画的页计费**：降级成静态段的页不计。整体退回静态版时结算 0（预占释放）。
- **失败与取消释放**：任务落为失败终态或取消会放掉预占；可重试的中间失败保留（否则重试成功后就漏账）。
- **改上限下一次导出即生效**：策略是上限唯一来源，受理时同步到额度表。

台账在 `tenant_quotas` / `quota_reservations` / `usage_ledger` 三张表，**`usage_kind` 带账期后缀**：

```
animation_minutes:2026-10
```

跨月是新行，所以月度汇总要按前缀聚合（`WHERE usage_kind LIKE 'animation_minutes:%'`），
不能等值匹配。恢复时间就是下月 1 日 00:00 UTC。

- **按内容命名，不按页码**：不同租户的第 3 页不会写进同一个文件（按页码命名时后写的
  会让前一次导出渲染出别人的页面，那是串租户）；同一份图重复导出只写一次。
- **原子落地**：同目录临时文件 + `rename`，渲染侧永远看不到半个文件。
- **权限**：目录 0755、文件 0644。两个容器常常不是同一个 uid，缺这几位会得到
  「导出侧写成功了，渲染侧 permission denied」——错误发生在渲染容器里，看起来像服务坏了。
- **目录只增不减**，它是缓存不是数据：删掉不丢东西，下次导出重写。清理按 mtime 定期 prune，
  不要放进导出路径（那会把一次导出变成一次全目录扫描）。

三项缺任一项，导出都**照常成功**、只是出静态版；启动日志会明确说是哪一项缺：

```
animation: PPTS_ANIMATION_ASSET_DIR is empty; page images cannot reach the renderer, exports stay static
animation: asset publisher disabled; exports stay static            # 目录不可写，启动即报
animation: page assembly wired via shared asset dir                 # 接好了（element_geometry=true）
```

`element_geometry` 说的是**元素几何有没有来源**（tasks 2.0b）。`true` 时它来自解析期已经落下的
`<tenant>/<project>/src-NN/document/extracted.json`——那份文档里本来就有每个形状的 EMU 框，
导出期按源版本读一次即可，不新增产物、也不重解析 PPTX。

`false` 只出现在没有对象存储的接线里（`wireAnimationPages` 拿不到 objects）。
此时**元素级轨道**（标题、要点、图表…逐个入场）会被编译成 `anchor_missing` 诊断并跳过，
**整页轨道**（转场、整页推拉/淡入）仍然生效——所以「没有几何」不等于「动画全废」。

几何读失败（解析产物缺失、文档损坏、源版本为 0）只影响那一页的元素轨道，日志里是
`animation: load element geometry failed, page only gets page-level tracks`。
看到它就查两件事：该源版本的 `document/extracted.json` 在不在、规格里的 `sourceRevisionNo`
是不是 0。

### 前端怎么知道"这次能不能出动画"

`GET /api/projects/{id}/exports/animation-options?timelineKey=<key>`

```json
{
  "enabled": true,
  "pages": [
    { "slideId": "slide-1", "language": "zh-CN", "sourceRevisionNo": 2, "revision": 5 },
    { "slideId": "slide-2", "language": "zh-CN", "sourceRevisionNo": 2, "revision": 0 }
  ],
  "hasTimeline": true
}
```

- `revision=0` 表示这一页没有规格（导出时按静态画面）。**不省略这一类页**——
  前端要能显示「5/12 页配了动效」，而不是让用户自己数。
- 前端把 `revision>0` 的项**原样**放进导出请求的 `animation.specs`。不在这里替它
  "顺便"提交最新版：导出是对「用户看到的那一版」的承诺，排队期间规格被改，
  该页会因 `Revision` 不符降级并留痕，而不是拿新版渲下去。
- `enabled=false` 时 `reason` 一定是这四个之一（前缀匹配，冒号后是详情）：
  `animation_not_available`（规格存储未上线）、`animation_disabled_by_deployment`、
  `renderer_not_configured`、`renderer_unavailable: ...`。

排查顺序：先看 `reason`，再看 worker 日志的降级 warn。两者说的是不同阶段的事——
`reason` 管「能不能勾」，worker 日志管「勾了之后为什么这版还是静态」。

### 换 S3 后端要动哪里

只换一个实现：`app.AssetPublisher`（`internal/app/animation_assets.go`）。

```go
type AssetPublisher interface {
    Publish(ctx context.Context, contentType string, data []byte) (relPath string, err error)
}
```

返回值的语义是「**渲染服务资产根下的相对路径**」，所以换实现时导出侧（组装、编译、拼接）
一行都不用改。S3 实现需要渲染侧同时具备下面任一条：

1. 渲染服务新增 `PUT /v1/assets/{path}`：导出侧上传，渲染侧落盘后走现有 staging；
2. 渲染服务直接以 S3 为资产根（`stageAssets` 改为从对象存储取 `baseImage`）。

两条都还没做，所以现在只提供本地盘实现。`PPTS_ANIMATION_ASSET_DIR` 在多机部署下
本来也不成立——共享卷的前提是导出与渲染在同一台机器上。

## 计划诊断（`plan.warnings`，出片前先扫一眼）

诊断不阻断渲染，但意味着「成片和输入不完全一致」。全部码定义在 `internal/render/ir.go`。

| code | 含义 | 怎么处理 |
|---|---|---|
| `sibling_overlap_partial` | 两个动画元素**互不包含但部分重叠**（重叠 ≥ 较小的那块 5%）。ADR-9 的抹除只对包含关系成立，这种情形两张层位图会互相烘进对方的像素，动谁另一个就跟着拖影（ADR-20） | **修不掉**（单张栅格推不出逐元素 alpha）。要么改版式把两块拆开，要么接受；告警里带第一对元素 id |
| `duration_tier_conflict` | 有 N 条 clip 时长不在视觉基线的元素速度分层内（catalog 区间优先，未改动时长） | 通常是预设的固有偏差，可忽略；大量出现说明预设或 catalog 需要调 |
| `anchor_missing` / `box_clamped` / `clip_truncated` / `segment_unresolved` / `geometry_inferred` / `transition_defaulted` | 锚点/几何/裁剪/分段/画布/转场回退 | 逐条看 message：几何被 clamp 或 clip 被截断通常会直接反映在画面上 |

`sibling_overlap_partial` 的 5% 门槛是刻意的：文本框包围盒普遍带内边距，标题盒与副标题盒
常有一两像素交叠且交叠区没有墨，纯几何判定会天天误报，告警也就没人看了。

## 降级矩阵（用户最终拿到什么）

动画是**增强层**：任何一环出问题都退回静态版，导出本身必须成功（ADR-21）。
所以排查「为什么这版没有动画」时，先看导出日志里的 warn，而不是渲染服务日志。

| 情形 | 用户拿到 | 留痕 |
|---|---|---|
| 开关关闭（`PPTS_ANIMATION_ENABLED` 未开） | 静态版，行为与上线前逐字节一致 | 无请求、无日志 |
| 输入组装器未接（底图投递未配置） | 静态版 | worker 启动时一条 `animation: enabled, but page assembly is not wired yet` |
| 元素几何读不到（解析产物缺失/文档损坏） | 该页只有整页动效 | `animation: load element geometry failed, page only gets page-level tracks` |
| 服务不可达 / 提交失败 / 等待超时 | 静态版 | `animation channel degraded to static export` + reason |
| 某一页渲染失败 | 该页静态段（服务端自己出），其余页动画 | `degradedPages` 里 `code=page_render_failed`；worker 记一条 `animation segment degraded to still` |
| 某一页静态重试也失败 | 该页由上游换静态段，其余页动画 | `code=page_skipped` + 上面的 `pages=[..]` |
| 整片渲染失败（一页都没出） | 静态版 | 同上 |
| 某一页没有片段 | 该页静态段，其余页动画 | `animation export contains still pages pages=[..]` |
| 某一页时长漂移 > 100ms | 换静态段**重拼一次**；仍漂移则整片静态 | `pages=[..] drifted=[..]` |
| 页面 4:3 但按 16:9 输出 | 静态版 | 编译期 `ErrAspectMismatch`（宁可降级，不出元素框错位的动画） |
| 部分页本来没有规格 | 那些页是静态画面，其余页有动画 | 不算降级，无需处理 |

两条容易误判的：

- **`pages=[..]` 里的页不是「渲染失败」**，而是「这一页最终用了静态图」。用户看到的是
  「这页突然不劲了」，不是错误提示。
- **`drifted` 是 `pages` 的子集**：少给一段是服务端问题，时长不准多半是计划与素材对不上，
  两者的修法完全不同，所以分开记。

成片行不记录这些信息（要加列得走迁移），日志里的 `plan_hash` 是把它和具体一次渲染对上的唯一线索。

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
