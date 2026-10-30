# 任务：成品包浏览器门禁进 CI

## 1 探针与环境解耦

- [x] 1.1 `bundle-probe.mjs` 自行解析浏览器：`PPT_CHROME` → 镜像路径 → 平台常见路径（Linux/macOS/Windows）；
      显式指定但不存在时**报错**而不是退到下一个候选（"我明明设了却没生效"比"找不到"难查）
- [x] 1.2 新增 `--print-chrome`：只解析并打印路径，供门禁选引擎用——候选表只有一处事实源
- [x] 1.3 CDP 通道改用 Node 22 全局 WebSocket；旧 Node 退回 `require('ws')`（镜像路径不变），
      两条路收成 `send/onMessage/close` 同一组方法
- [x] 1.4 `file://` 用 `pathToFileURL`（`path.posix.join` 在 Windows 上拼出 `file://C:\...`，Chrome 直接拒收）
- [x] 1.5 独立 `--user-data-dir`：本机已有 Chrome 在跑时，新进程会把参数交给既有实例后退出，
      `--remote-debugging-port` 不生效，报错只剩"did not expose a debugging endpoint"
- [x] 1.6 成功/失败两条路径都收摊（kill 子进程 + 删临时 profile）

## 2 样例包要有可测对象

- [x] 2.1 `share-baseline -layers N`：每页合成 N 个图层，依次淡入 + 上移 24px，带 `fade` 360ms 转场
- [x] 2.2 动画时长 900ms（不是 520ms）：门禁在页首 +400ms 取样，探针实际落在 500–700ms，
      520ms 的动画在那里只剩 0.0005px 位移——判据靠千分之一像素成立等于没测
- [x] 2.3 体积分项把层图从底图里分出来（`slides/*-layer-*`）：真实包里层图占八成，混在底图里看不见
- [x] 2.4 实测记录：**不带图层的包跑门禁会红四项**（默认态无位移、reduced 无 opacity、
      时长集合为空、无 WAAPI 无层）——所以「进 CI」与「基线能造真图层」是同一件事

## 3 门禁可换引擎

- [x] 3.1 `bundle-gate.py --engine auto|native|docker`；`auto` = 本机 Chrome 优先，失败退 docker，
      两者都没有则以退出码 2 说明缺什么
- [x] 3.2 报告头与 `--json` 都写出实际用的引擎与浏览器路径（CI 日志里能回答"到底是哪个浏览器判的"）

## 4 CI

- [x] 4.1 新增 `share-browser` 作业（`needs: lint`）：setup-go + setup-node 22 → 解析浏览器 →
      `share-baseline -layers 2` → `bundle-gate.py --engine native`
- [x] 4.2 浏览器缺失时按需拉 `chrome-headless-shell@stable`（与渲染镜像同一个二进制），
      不让 runner 镜像的预装情况决定这条门禁跑不跑

## 5 验证

- [x] 5.1 正例：3 页 / 12s / 2 层 / fade 转场，21 条判据全 PASS（本机 Windows + Chrome 148）
- [x] 5.2 负例一（探针级）：把 `slides/002-layer-00.png` 写成非图片字节 → 结构检查仍通过，
      只有 V16「清单资源全部可加载」红，退出码 1
- [x] 5.3 负例二（判据级）：用不带 `-layers` 的包 → V17 红四项，退出码 1
- [x] 5.4 负例三（环境级）：`PPT_CHROME` 指向不存在的文件 → 退出码 2 且报文指明缺浏览器
- [x] 5.5 `go build ./...` / `go vet ./...` / `go test ./...` / `gofmt -l cmd internal scripts` 干净
- [x] 5.6 `openspec validate --all` 通过

## 6 文档

- [x] 6.1 路线图 §4 覆盖表：V16/V17 从「否，只在本地」改为「是（`share-browser` 作业）」
- [x] 6.2 规格修正：`html-share-quality` 里 `bundle-gate.py --scan` 这个参数**不存在**（实际是 `--skip-browser`）
