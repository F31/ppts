# html-share-quality Specification

## Purpose
定义「HTML 成品包」在交付前必须通过的质量判据：自包含（V14）、体积预算（V15）、真实浏览器可播放（V16）、四条降级路径可用（V17）。目标是让「包有没有坏」变成一条可执行的命令，而不是靠人打开看一眼——外链、体积翻倍、资源 404、降级失效这四类问题在导出时都不会报警，只会在客户打开时暴露。

## Requirements

### Requirement: 外部引用数为零

成品包 MUST 自包含。包内所有文本资源（`index.html`、`app.css`、`app.js`、`manifest.js`、`manifest.json`、`subtitles.js`、`timeline.json`）中出现的 `src=`、`href=`、`url(...)`、`@import` 目标，MUST 全部解析为包内相对路径。MUST NOT 出现 `http://`、`https://`、协议相对 `//host`、`file:` 绝对路径或包外绝对路径。

打包时 MUST 自检并在违规时失败（不产出坏包）；门禁 MUST 对**产物目录**再扫一次。

#### Scenario: 打包时拒绝外链

- **WHEN** 某条模板或播放器产物中出现 `https://cdn.example.com/font.woff2`
- **THEN** 打包 MUST 失败并指出违规条目与片段，MUST NOT 生成该包

#### Scenario: 门禁扫描产物

- **WHEN** 对一个已知自包含的包运行 `bundle-gate.py --skip-browser`（只跑 V14/V15）
- **THEN** 外部引用数 MUST 为 0；若被人为往 `index.html` 里插入一个 CDN `<script src>`，扫描 MUST 报出该条

#### Scenario: 协议相对 URL 也算外链

- **WHEN** 出现 `src="//cdn.example.com/a.js"`
- **THEN** MUST 判定为外部引用（`//` 会被浏览器解析成当前协议下的外部主机）

### Requirement: 可播放性由真实浏览器验证

门禁 MUST 用无头浏览器加载成品包并验证：播放器挂载成功、清单引用的每个页图/层图/音频/字幕资源都能取到（无 404、无解码失败）、音频时长与清单 `artifact.durationUs` 之差 ≤ 0.25 秒、seek 到末尾后 `play()` 能触发 `ended`、全程无 console error 与资源加载失败。

验证 MUST NOT 修改被验的包；需要注入探针时 MUST 在临时副本上进行，并在前后比对原包哈希。

门禁 MUST 在每次 push 与 PR 的 CI 中执行。浏览器 MUST 由门禁自行解析（`PPT_CHROME` 优先，其次平台常见路径，最后才是渲染镜像），MUST NOT 要求先构建渲染镜像才能运行。环境里确实没有可用浏览器时，门禁 MUST 以退出码 2 失败并说明缺什么，MUST NOT 静默跳过、MUST NOT 记为通过——「没跑」与「跑了通过」在报告里必须是两件事。

#### Scenario: 样例包必须带图层

- **WHEN** 用不带 `-layers` 的包跑门禁
- **THEN** 降级判据 MUST 失败，MUST NOT 悄悄通过——四条判据全落在 `.ppts-layer` 上，
  没有图层的包既验不出降级

#### Scenario: 正常包通过

- **WHEN** 对一份 5 页 / 30 秒 / 含混音轨的包运行门禁
- **THEN** 报告 `mounted=true`、`resources.failed=[]`、`ended=true`、`consoleErrors=[]`，判定通过

#### Scenario: 资源缺失必须失败

- **WHEN** 副本中删除清单引用的某张层图
- **THEN** 门禁 MUST 失败并把该路径列入 `resources.failed`，MUST NOT 因为「画面看起来还行」而通过

#### Scenario: 探针不能靠内联脚本

- **WHEN** 需要注入检测脚本
- **THEN** MUST 以同目录外部 `.js` 文件引入（产物 CSP 为 `script-src 'self' file:`，内联会被拒绝）

#### Scenario: 环境里没有浏览器

- **WHEN** 运行环境既没有 Chrome，也没有可用的渲染镜像
- **THEN** 门禁 MUST 以退出码 2 结束，并说明「设置 `PPT_CHROME` 或提供镜像」
- **AND** MUST NOT 输出「PASS」——没有执行过的判据不得计为通过

#### Scenario: 样例包不带图层

- **WHEN** 用一份所有页都 `layers` 为空的包跑门禁
- **THEN** 「默认态存在位移」「无 WAAPI 时层为终态可见」「reduced-motion 时长不变」等判据 MUST 失败
- **AND** 报告 MUST NOT 因为「本来就没有层」而把这些判据判为通过

### Requirement: 降级矩阵逐条可执行

以下四种条件 MUST 各有自动化用例，且判据为「页面可读」而不是「没有抛错」：

| 条件 | 判据 |
|---|---|
| 禁 JS | `noscript` 内页图数量 ≥ 页数、每张 `naturalWidth > 0`、讲稿文本非空 |
| 无 WAAPI | 层元素 `transform` 为 `none`、`opacity` 为 `1`（终态可见） |
| `prefers-reduced-motion: reduce` | 层 `opacity` 仍随时间变化、无位移/缩放、动画时长与默认态一致 |
| 低内存（`deviceMemory < 4`） | 不创建层元素，页面仍渲染当前页与字幕 |

#### Scenario: 禁用 JavaScript 仍可读

- **WHEN** 以禁用脚本的方式打开成品包
- **THEN** 页面 MUST 呈现标题、逐页静态图与对应讲稿文本，MUST NOT 只显示一句「需要启用 JavaScript」

#### Scenario: reduced-motion 不改时长

- **WHEN** 浏览器声明 `prefers-reduced-motion: reduce`
- **THEN** 位移与缩放 MUST 被去掉，`opacity` MUST 保留，动画总时长 MUST 与默认态相同（缩短会造成音画错位）

### Requirement: 体积构成可测量

打包器 MUST 能给出成品包按类别（页图、层图、音频、播放器、字幕、清单）的字节数与动画页数。门禁 MUST 按 `3 MiB + 0.55 MiB/分钟` 判定预算，超预算时 MUST 告警并列出构成明细，MUST NOT 静默通过。

门禁报告 MUST 明确标注未实现项（字体子集、WebP、AAC），MUST NOT 对未实现项给出「通过」。

#### Scenario: 报告可定位超标项

- **WHEN** 包体积超预算
- **THEN** 报告 MUST 给出各类别字节数与占比，使人能判断是音频大、页图大还是层图未合并

#### Scenario: 未实现项不得伪装通过

- **WHEN** 包内没有内嵌字体（当前实现的常态）
- **THEN** 「字体子集覆盖率」MUST 标注为 `not_applicable`，MUST NOT 记为 `pass`

### Requirement: 门禁报告的引擎可追溯

门禁输出（人类可读与 `--json` 两种）MUST 记录本次判定所用的引擎（`native` / `docker`）与浏览器路径或镜像名。同一份包在不同浏览器上判出的结果不同时，读报告的人 MUST 能立刻看出是哪一次、哪个浏览器判的。

#### Scenario: 两条引擎路径

- **WHEN** 同一份包分别用本机 Chrome 与渲染镜像跑门禁
- **THEN** 两次报告 MUST 各自写明引擎与浏览器版本/镜像名，判据条目与阈值 MUST 完全一致
