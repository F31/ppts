# Tasks

## 1. 编号订正与设计文档

- [x] 1.1 核实 V18/V19 在 CI 与脚本中的实际占用（`ci.yml:123/147`、
      `scripts/review/seam-gate.mjs`、`shot-gate.py`），确认改埋点编号成本最低。
- [x] 1.2 `docs/动画视频扩展-设计方案-V1.0.md`：§20.7 引用改为 V20、§20.8
      标题改 V13–V20、表格补 V13b/V18/V19 并把埋点移到 V20、M6-4 验收同步，
      加编号订正注记。
- [x] 1.3 项目设计文档原件 `动画视频扩展-设计方案-V1_0.md` 同步同样的最小订正
      （该目录不是 git 仓库，改动不进 commit）。

## 2. 匿名上报端点

- [x] 2.1 `internal/api/artifact_share_events.go`：`shareViewReport` /
      `normalizeShareViewReport` 封闭事件集合、单请求去重、来源白名单。
- [x] 2.2 `shareEventHandler.serve`：同源 Origin、Content-Type、body 上限、
      链接有效性、口令门、按 token 限流、静默 204。
- [x] 2.3 `shareEventThrottle`：进程内固定窗口 40 次/分钟 + 4096 容量帽与过期淘汰。
- [x] 2.4 复用 `audit.Recorder` 写 `artifact_share.view_event`；不新增迁移、
      不新增匿名可写表、不记 token/IP/UA。
- [x] 2.5 `registerArtifactShareAnonRoutes` 注册 `POST /s/{token}/events`，
      与 GET 通配按方法分开；`web/route-inventory.json` 的 `share-serve` 组 reason 同步。

## 3. 播放器与 CSP

- [x] 3.1 `web/src/sharePlayer/telemetry.ts`：纯函数层（启用判据、同源端点、
      来源提取、进度跨桶、拖动跳过、匿名 session）。
- [x] 3.2 `main.ts` 的 `startShareTelemetry`：400ms 微批、`keepalive`、
      `pagehide` 冲刷、单处 `fetch`、失败静默；四处接入点
      （reportView / tick / seek / finish）。
- [x] 3.3 `internal/share/html.go` 的 `indexCSP` 补 `connect-src 'self'`，
      注释写清两份 CSP 取交集与 V14 不受影响。
- [x] 3.4 `npm run build:share` 重新生成 `internal/share/assets/app.js`（20,781 字节）
      并纳入提交（CI 有 `git diff --exit-code` 漂移检查）。

## 4. 测试与门禁

- [x] 4.1 `internal/api/artifact_share_events_test.go`：五类事件齐全、去重、
      非法事件零写入、总闸/Origin/Content-Type/无效 token/口令门/限流窗口、
      脱敏字段核对（`TestShareEvents*` 与 `TestNormalizeShareViewReport*`）。
- [x] 4.2 `web/share-telemetry-check.mjs` 9 项：在线/离线与隐私判据、端点同源、
      来源脱敏、跨桶与拖动跳过、前后端事件集合一致、源码接线、CSP、V14 未放宽、
      编号订正；挂进 `npm run build` 的 `telemetry:test`。
- [x] 4.3 `scripts/review/telemetry-gate.mjs` 8 条成品包判据进 CI；
      用 `share-baseline` 造包实测 8/8 通过，换回旧版 app.js 负例 6 红 exit 1。
- [x] 4.4 `.github/workflows/ci.yml` 新增「埋点完整性门禁（V20）」步骤，
      V18/V19 两步保持原样。

## 5. 全量验证与交付

- [x] 5.1 全量验证：`go build/vet/test ./...`（全包 ok）、`npm run build`（门禁组 +
      `tsc -b` + `vite build`）、`npm run build:share`（`app.js` 20,781 字节，重跑无漂移）、
      `npm run i18n:check`（中英 1653/1653）、`openspec validate --all`（12/12 通过）、
      `git diff --check`。**如实登记**：`--all --strict` 另有 5 份主 spec 的
      「Requirement 文本 >500 字符」WARNING，是 HEAD 既有条目、非本变更引入；
      本变更 `validate <change> --strict` 单独通过。
- [x] 5.2 8077 预览（SQLite）与 8080 主实例（Postgres）均已运行含 V20 的二进制：
      8077 走完整端到端，8080 的 `/s/{token}/events` 返回带 `connect-src 'self'`
      的 handler 404（与路由器纯文本 404 对照），证明两实例路由都已注册。
- [x] 5.3 **在线分享真机端到端**：`tmp/verify_share_telemetry.py` 24 项全过。
      预览库现有成品都是 mp4，无法直接建在线分享链接，故插入一条 html_bundle
      成品行 + 用 `v20bundle.zip` 当包体，链接走**真实创建 API**、事件走**真实 HTTP**、
      审计走**真实表**；验完撤销并清理全部夹具行，行数回到验证前。覆盖：302 尾斜杠、
      两侧 CSP、五类事件落 5 条匿名审计（单请求去重）、脱敏字段、撤销即失效、
      七类负例零写入、负例零审计。**边界**：`?privacy=1` 零写入、口令门未解锁 404、
      限流窗口由前端纯函数用例与 Go `TestShareEvents*` 覆盖，不冒充真机验证。
- [x] 5.4 OpenSpec 变更归档（+3 条 Requirement），工作日志补记。
- [x] 5.5 一次 commit + push，核对远端 CI。

## 6. 实现期发现

- [x] 6.1 **CI 阻断项**：`web/share-telemetry-check.mjs` 的编号订正用例原本直接
      `read('../docs/动画视频扩展-设计方案-V1.0.md')`，而 tasks 6.4 已把 `/docs/*`
      移出仓库（`.gitignore` 只放行 `adr/`）——本地绿、CI 干净检出会 ENOENT 判红。
      改为只锚定随代码分发、且不受 `openspec archive` 移动的三个文件
     （`ci.yml` / `telemetry-gate.mjs` / `route-inventory.json`），设计文档改为
      **存在时**才补核；临时移走该文档模拟干净检出实测 9/9 通过。
- [x] 6.2 **编号命名空间**：保真链 `scripts/fidelity/verify.py` 另有一组 V20
      （矩形焦点遮罩，判据码同样形如 `V20.x`，`check_speech_gate.py` 还有一组同号判据）。
      V20 只在 M6-4 门禁体系内唯一；在 §20.8 两份设计文档、CI 注释、
      `telemetry-gate.mjs` 与 design.md 各加一条命名空间注记，不改编号
      （改成 V21 会让 M6-4 序列凭空跳号，制造新的困惑）。
