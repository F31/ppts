## 1. 选型与契约

- [x] 1.1 逐条核对 ffmpeg 对 IR 六种属性（opacity / translateX / translateY / scale / scaleX / scaleY）的可行性，结论与依据写入 design ADR-1
- [x] 1.2 定分层：L0 页转场用 ffmpeg `xfade`、不启动服务；L1 元素入场走 Remotion
- [x] 1.3 `Page.plateImage` 可选字段：路径校验、排除出 `planHash`、IR 校验覆盖
- [x] 1.4 `/healthz` 增加 `chromeVersion`（后端可选接口上报，服务层不感知 Chromium）

## 2. 合成器（`renderer/`）

- [x] 2.1 `src/types.ts`：`CompiledTimeline` 的 TS 镜像，字段与 `internal/render/ir.go` 一一对应
- [x] 2.2 `src/motion.ts`：cubic-bezier 求解（Newton + 二分兜底）与属性叠加（opacity 乘、位移加、缩放乘）
- [x] 2.3 `src/Timeline.tsx`：页序、页间转场（fade/slide/zoom）、图层裁剪与变换
- [x] 2.4 `src/index.tsx`：`Composition` + `calculateMetadata`（时长/画布来自 props）
- [x] 2.5 `render.mjs`：逐页 `frameRange` 渲染，stdout 输出 JSON 行进度的，固定 CRF/GOP/像素格式
- [x] 2.6 `still.mjs`：单帧导出，用于排查残影/转场过冲
- [x] 2.7 `tsc --noEmit` 通过、`npm ci` 依赖锁定（`package-lock.json` 入库）
- [x] 2.8 Studio 预览的「分辨率/颜色」改为可复跑核对（ADR-19）：Studio 是交互式界面，无头环境打不开、
      也无人眼可用；而它预览的画面就是 `src/index.tsx` 注册的 `Timeline` composition，所以用 `still.mjs`
      渲染同一帧 + `scripts/review/still-check.py` 逐像素比对（`--settled` 要求与源页相同，
      `--confined-to` 要求动画中的差异只落在元素框内）。2026-10-29 实测 5 页 1920×1080 稳定帧
      **全部逐像素相同**（平均差 0.000 / 差>32 像素 0），样本含深色页 `#0E1B2E` 与渐变页；
      第 10 帧差异 bbox 完全落在标题框内；反向验证（拿错页 → 平均差 19.7；框缩小 → 23545 像素越界）
      确认检查不是空转。Studio 交互面板本身不是交付物，未纳入
- [ ] 2.9 与 DOM 端（M6-2）抽出共享插值实现，避免两份 cubic-bezier 各自演化

## 3. Go backend

- [x] 3.1 `BuildPlate`：盒外环逐通道中位色补洞 + 极差判定（纯色底 0 色差、渐变底出 warning）
- [x] 3.2 `RemotionBackend.Render`：staging（底板 + `plan.json`）→ exec Node → 逐片段容器校验
- [x] 3.3 `workPath` 二次拦截绝对路径与 `..`（IR 校验之外的最后一道关口）
- [x] 3.4 `cmd/renderer` 支持 `-backend remotion`、`-renderer-script`、`-chrome`、`-render-timeout`
- [x] 3.5 `-selftest`：2 页固定样例走完整链路，失败非零退出；stub backend 明确拒绝
- [x] 3.6 单测：补洞语义、不修改调用方计划、噪声判定、脚本缺失/Node 缺失的错误信息、路径逃逸、MP4 容器校验
- [ ] 3.7 单页失败只降级该页（当前整次渲染失败）：需 M3 的降级矩阵接线后才有意义
- [x] 3.8 `clip.startMs` 与触发锚点相加（ADR-10）：此前 `startMs` 被静默丢弃，错落编排全部塌缩到 0ms
- [x] 3.9 嵌套元素按层出图（ADR-9）：`BuildLayerAssets` 只抹「被本层包住的子元素」，填色取自原图；IR 增加可选 `layer.image`，合成器直出
- [x] 3.10 失败/取消不再毒化 `planHash`（ADR-11）：此前终态非成功仍占着 `byHash`，同一份计划失败一次就永远渲染不出来；取消立刻释放，重试分配新 jobId + 新目录
- [x] 3.11 `DELETE /v1/render/{id}` 回实际状态：对已成功任务点取消，此前硬编码回 `canceled`，与实际不符
- [x] 3.12 排队上限（ADR-12）：`MaxQueued`（默认 8）只约束 `queued`；超限返回 429 + `Retry-After`（码 `queue_full`），在读 body 之前先判、锁内入队前再判；被拒提交不建 job、不消耗重试编号
- [x] 3.13 终态保留（ADR-12）：`JobTTL`（默认 30 分钟，从落终态起算）+ 请求路径上的惰性回收；过期删记录、释放 `byHash`、删产物目录；`attempts` 用 live 计数，归零即删
- [x] 3.14 重启后 `/work` 下的孤儿目录无人回收（`jobs` 只在内存）：选择「保持无状态 + 启动清扫」（ADR-13），`NewService` 只删匹配 `ShortID(-rN)` 形态的目录，外来条目（`selftest-*`、`lost+found`、普通文件）不碰；清扫失败不阻塞启动；多副本共享 workdir 用 `-keep-orphans` 关掉

## 4. 镜像

- [x] 4.1 `deploy/renderer/Dockerfile`：Go 构建段 + base（Node/Chrome/字体）+ app 段，非 root
- [x] 4.2 `CHROME_VERSION` 单一事实源 + `pin-chrome.sh`（下载并写 `chrome.sha256`）+ 构建期摘要校验
- [x] 4.3 `compose.yml`：只读 rootfs、tmpfs `/work`、`network_mode: none`、CPU/内存/PID 限额、selftest 服务
- [x] 4.4 `.dockerignore`：排除 `node_modules` 与 `web/`，避免上下文膨胀
- [x] 4.5 生成并提交 `chrome.sha256`：`156.0.8078.73` / linux64 / chrome-headless-shell，126,938,225 字节，`sha256=77eb8d52…`；摘要与 `Content-Length`、GCS `x-goog-hash: md5=`、zip 条目（287 项含可执行文件）三重核对。同时修掉 `pin-chrome.sh` 在 Git Bash 下写出 `\<hex>`（coreutils 转义前缀）的问题，并加 `.gitattributes` 固定该文件为 LF
- [x] 4.6 镜像首次 `docker build` + `-selftest`：在 WSL2（Ubuntu 24.04 / docker 29.4.1）上跑通，
  `ppts/renderer:dev` 1.36GB，`selftest ok: backend=remotion pages=2 elapsed=13.954s`。暴露并修掉 3 个真实缺陷：
  ①`node:22-bookworm-slim` 缺 Chrome 运行时库——镜像本身"构建成功"，但 `chrome-headless-shell --version`
  报 `libglib-2.0.so.0: cannot open shared object file`，补齐 libnss3/libnspr4/libatk*/libcups2/libgbm1/
  libasound2/libx* 等依赖；②`package-lock.json` 里 `@remotion/compositor-*` 缺 `libc` 字段，npm 在 Debian
  上装成 musl 变体（运行期 `Cannot find module '@remotion/compositor-linux-x64-gnu'`），补 4 条 `libc: glibc|musl`；
  ③自检命令只挂 `/work` 且未给 `mode=1777`，UID 10001 写不进去、Remotion 又在只读 `/tmp` 上 `EROFS`，
  README/compose/Dockerfile 统一改为双 tmpfs。网络侧：代理对大文件与长连接不稳定（Chrome 121MB 走代理
  6 次全是 `curl: (18)`、`npm ci` 报 `ECONNRESET`），`NO_PROXY` 必须排除 `storage.googleapis.com` 与
  `registry.npmjs.org`，且 curl/apt/npm 三步各自带重试
- [x] 4.7 解决 `textnorm@v0.2.2` 校验和不一致对 `GOWORK=off` 构建的阻塞：`go.sum` 记的摘要与 sumdb 权威值不符（v0.2.0/v0.2.1 同样错），已按 `h1:IZDOaanu…` 修正；`GOWORK=off go build/test ./...` 跑通（42 包）
- [x] 4.8 字体子集化（ADR-15）：实测全量是 **91MB** 不是 9MB（4 个 TTC：Sans/Serif × Regular/Bold，
  每个 TTC 含 SC/TC/JP/KR/HK 五个地区字形）。边界落 `deploy/renderer/charset.txt`（唯一事实源），
  分 `must`（汉字/CJK 标点/全角/假名/拉丁，缺一个构建失败）与 best-effort（符号区，缺了走 fallback）两档；
  汉区取 URO 而非 GB2312——实测 喆/堃/犇/垚/玥/珺/赟/燚/弢/韡/祎 这些人名用字 GB2312 全不覆盖。
  `subset-fonts.py` 在独立构建段跑（`python3-fonttools` 不进运行时），产物两个 OTF 共 21.89MB；
  字体版本由「实际保留码位集」的摘要推导（`2.004-sub03e7e8d7`），经 `/etc/ppts/fonts.version` →
  `entrypoint.sh` → `-fonts` → `planHash`，不再手写日期。构建期三道门禁：must 覆盖率、
  子集后家族名不变、`fc-list` 能精确匹配到 `Noto Sans CJK SC`（最后一条挡「装了但 fontconfig 认不出」）。
  验收：`/usr/share/fonts` 91MB→24MB、镜像 1.36GB→1.23GB、`/healthz.fonts` 报
  `2.004-sub03e7e8d7`、同一样片 5 段 sha256 与旧镜像逐字节相同、`selftest ok … elapsed=9.42s`
- [x] 4.9 子集丢空格修复（ADR-15 §7）：`populate` 只喂「可绘制」码位，导致 U+0020 与 U+3000
  从 cmap 消失——用这份字体排中文时每个空格都是方框。改为喂「源字体真实拥有的全部声明码位」，
  断言仍只查可绘制部分；版本摘要改为覆盖实际保留集（否则改脚本不改 charset 时 planHash 不变）
- [x] 4.10 图层绘制顺序（ADR-17）：`BuildLayerAssets` 会把子元素从父层位图抹掉，而合成器按数组
  顺序画。只把 `p1-card` 挪到末尾、内容一字不改，三行要点墨量从 5.70/6.07/6.03% 掉到 0.00%——
  任务仍然 `succeeded`、无 warning。已在 `Compile` 与 `stage` 两处用稳定排序保证「父先于子」。

## 5. 验收

- [x] 5.1 5 页 / 30s 样片在本机 Chrome 上真实出片（5 × 6s，共 900 帧；首轮 1m58s，修 ADR-10 后重渲 1m29s）
- [x] 5.2 片段核对：5 个 `segment_*.mp4` 均为 1920×1080 / 6.000s / 180 帧 / `ftypisom` 容器
- [x] 5.3 残影验证：同一帧「有底板 / 无底板」对照，无底板时标题条在 t=67ms 已完全不透明（动画视觉失效）
- [x] 5.4 容器内跑同一份 30s 样片（依赖 4.6）：5 段均 1920×1080 / 180 帧 / 6.000s，提交到终态 54s；
      两次独立渲染 sha256 完全相同；t=0 纯白、起播点 733/933/1167ms（声明 700/920/1140）全部在 1 帧内。
      这条同时暴露并修掉了「HTTP 契约里底图无处安放」——见 ADR-14、`internal/render/assets.go`
- [ ] 5.5 5 页样片观感盲评（对照参考片，按视觉基线 §19 的评分口径）
      —— **卡在缺输入，不是卡在实现**：参考片与 §19 正文都在仓库外的方案文档里，仓库内没有可比对的参考片。
      已把能做的部分做掉：`scripts/review/RUBRIC.md` 给出 6 项评分表（字形完整 / 文字清晰 / 元素完整 /
      残影与转场 / 时序与节拍 / 色彩与对比），只覆盖渲染器自己负责的部分（§19.1 里渲染引擎 ~5% +
      运动质感 15%）；`scripts/review/storyboard.sh` 把成片压成联系表，30s 样片 10 秒看完。
      待人工：两人独立打分（同一项分差 ≥2 分回看对应秒数），记录格式见 RUBRIC §四
- [x] 5.6 30 页 / 5 分钟长片压测（并发、内存峰值、OOM 边界）：30 页 = 9000 帧 / 300s，
      双任务并发出 60 段。2 核下单任务 943s / 峰值 2666MB / PIDs 389；4 核下双任务 948s /
      4826MB / 783。**三处估算同时被推翻**：`mem_limit 2g` 在第一页就 OOM、`pids_limit 256`
      在第 4 页以 `write EPIPE` 崩掉、默认 `-render-timeout 15m` 装不下 943s。修正值与容量模型见
      ADR-16；`-render-timeout` 改为按片长推导（`6×片长+5min`，上限 6h），
      修掉了「长片必然被超时掐断」这个只靠常量永远解决不了的问题
- [x] 5.7 逐帧核对 t=0 无静态副本残影（纯白底板），收敛曲线 24.73 → 0（1000ms 内）
- [x] 5.8 编排忠实度：修 ADR-9 后三条 bullet 依次在 ≈800/1000/1150ms 出现（声明值 700/920/1140），卡片区域为卡片底色而非白条
- [ ] 5.9 部分重叠（互不包含）的兄弟元素仍会互相带出：单张栅格推不出逐元素 alpha，属模型限制
- [x] 5.10 带真实文字的 5 页样片端到端出片（2026-10-29）：底图用子集字体排中文（含生僻字
      喆堃犇垚玥珺赟燚弢韡祎、箭头/希腊字母/圈号），走 HTTP 全链路 97s 出 5 段。
      末帧 vs 源页逐像素：平均差 0.56–1.23、差 >32 的像素 <0.35%、元素区域墨量比 ≥0.98，
      属 H.264 噪声，**无结构性丢失**（ADR-18）。判据已做成可复跑脚本：`scripts/fidelity/`
      （`gen_pages.py` 造底图、`plan/` 编计划、`compare.py` 比对并在超阈值时非零退出）

## 6. 文档

- [x] 6.1 `design.md` 决策记录（ADR-1 选型核对表、ADR-6 残影、ADR-7 编码参数、ADR-9 嵌套裁切、ADR-10 起点语义）
- [x] 6.2 `deploy/renderer/README.md`：构建、自检、版本格式、已知阻塞
- [x] 6.3 路线图更新（M2 从「阻塞于容器」改为「本机已出片，剩镜像构建」）
- [ ] 6.4 方案文档回写（`docs/` 已移出仓库，改在变更归档时并入 `openspec/specs/`）

## 落地状态（2026-10-28）

**已完成：选型、合成器、Go backend、镜像定义、样片出片与逐帧核对（本机）。**

本轮最重要的产出不是 Dockerfile，而是**在真渲染时发现并修掉的三个缺陷**：

1. **ADR-6 残影**（已修）：底图已烘焙元素，抬升成图层后原位置仍留着本体 → `fadeIn` 视觉完全无效，
   `slideIn`/`pop` 出现双影。证据：同一帧（t=67ms），无底板时标题条已 100% 不透明；有底板时约 25%。
   修法：staging 用「盒外 1px 环的中位色」填平元素区域，产物作为 `page.plateImage`（可选字段，不入 `planHash`）。
2. **ADR-10 `startMs` 丢失**（已修）：`compileClip` 只取触发锚点，`clip.startMs` 被静默丢弃，
   编排的 700/920/1140ms 全部塌缩到 0ms。修法：`start = 锚点 + startMs`，补 2 个用例。
3. **ADR-9 嵌套元素被大图层带出**（已修）：卡片层裁切图里烘着三条件 bullet，bullet 完全跟随卡片的
   不透明度。证伪：把 bullet 的 `startMs` 改成 3000 重出同帧，2073600 像素零差异。
   修法：`BuildLayerAssets` 为每层产出「抹掉被自己包住的子元素」的位图，填色取自原图，
   IR 增加可选 `layer.image`，合成器直出。修后起播点 ≈800/1000/1150ms，与声明值一致。

**已知未修（ADR-9 边界）：部分重叠的兄弟元素。** 「包含」关系已经处理（`BuildLayerAssets` 只抹被本层包住的
子元素，填色取自原图），但两个部分重叠、互不包含的兄弟元素仍会互相带出。单张栅格推不出逐元素 alpha，
要彻底解决需上游提供分层素材；这条是模型限制，不是实现遗漏（5.9）。

**未完成：5.5 观感盲评（需要人看片）。** 4.5 / 4.6 / 4.8 / 5.4 / 5.6 已于 2026-10-28 落地：
`chrome.sha256` 已提交；镜像在 WSL2 的 docker 上构建成功、自检 13.954s 通过；同一份 5 页 / 30s
样片在容器内走完 HTTP 全链路出片（5 × 6.000s，两次渲染字节相同）；30 页 / 5 分钟长片在
双任务并发下出 60 段。5.4 暴露并修掉了「底图怎么进服务」这个契约缺环（ADR-14），
5.6 则把 compose 里三个估算值全部证伪（ADR-16）。剩余阻塞登记在 `deploy/renderer/README.md`。

**4.8 的结论和原来的任务描述不一样，值得单独记一笔。** 任务写的是「装全量 Noto CJK，约 9MB」，
实测是 **91MB**；而合成器 `Timeline.tsx` 只合成 PNG，**根本不排版文字**——把 `/usr/share/fonts`
挂成空目录重渲，5 段 sha256 与全量字体时完全相同。所以子集化省下的 67MB 换来的不是性能，
是**缩小一块永远用不到的攻击面**；字体在这个服务里的真实身份是「契约」而不是「渲染依赖」。
边界因此不能拍脑袋：`charset.txt` 分成 `must`（缺一个就构建失败）与 best-effort 两档，
汉字取 URO 而不是 GB2312（实测 13 个真实人名用字 GB2312 全不覆盖）。
构建期三道门禁里最有用的是 `fc-list` 精确匹配——它挡的是「字体装了但 fontconfig 认不出」
这种 `/healthz` 说没问题、实际走 fallback 的失败。

顺带修掉一个与字体无关的部署缺陷：`compose.yml` 的 healthcheck 用 `wget`，而镜像里从来没有 wget
（apt 列表只有 `curl unzip`），这条探针一直返回 127、容器永远 unhealthy。已改成 `curl -fsS`。

**5.6 的结论同样和预期不一样。** 压测前 compose 上写着「单 job 峰值内存 ~2GB」，于是限额就设成
`mem_limit: 2g` / `pids_limit: 256`——**把峰值当成了上限**。实测单任务峰值 2666MB，2g 在第一页就 OOM；
PIDs 单任务 30 页要 389，256 让渲染在第 4 页以 `write EPIPE` 崩掉（报错里没有一个字提到进程数）；
而 5 分钟长片在 2 核下要 943s，默认 `-render-timeout 15m` 会在第 27 页把它掐断。
三处一起错，是因为它们互相耦合：`-concurrency` 同时喂「任务数」与「帧并发」，满载是 `concurrency²`
个渲染位，成本是乘积。结论写进 ADR-16，并把超时从常量改成按片长推导——
判据是「长片长度由内容决定」，任何固定值都只是把边界换个地方出现。

**2026-10-29 追加：上一轮的「字体子集」结论有一半是假的，已修。**
4.8 的像素等价是拿**色块样片**验的——画面里一个字形都没有，所以它只证明了「合成器不依赖字体」，
证明不了「子集字体能正确排版」。换成带真实中文的底图后立刻暴露：所有空格位置都是方框。
缺的正是 `U+0020` 与 `U+3000`——`NON_DRAWING` 把它们排除出 `required`，而 `populate` 只喂了 `required`。
断言口径（可绘制）被当成了产出集合（应含不可绘制但影响排版的码位）。修法与实测见 ADR-15 §7。

同一次核查还抓到一个更隐蔽的丢失：**图层绘制顺序**。`BuildLayerAssets` 会把子元素从父层位图抹掉，
而合成器按数组顺序画；父层排在子层之后，子元素就被整块盖回底色。把 `p1-card` 从第 2 位挪到末尾、
其余一字不改，三行要点的墨量从 5.70/6.07/6.03% 变成 **0.00%**，任务仍然 `succeeded`、没有 warning。
修法是把「父先于子」做成稳定排序并在两处调用，见 ADR-17。

这两条共同的教训：**能自动判失败的东西，必须是对真实输入敏感的判据。** 色块样片、只查可绘制码位、
写在注释里的顺序约定，三者都「看起来有」但都不拦人。判据落在 ADR-18。
