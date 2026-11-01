# 任务：静态盒补偿（carry）

## 1. 编译与数据结构
- [x] 1.1 `render.Motion` 增 `Carry bool`（`carry,omitempty`）；`share.Clip` 同步增 `carry`
- [x] 1.2 `render/carry.go`：`CarryProps(prev, next Box, origin)` 按 §2 数学产出属性，恒等分量不写，同盒返回 nil
- [x] 1.3 `render/carry.go`：`CarryDuration(remaining, pageMS)` = `min(500, remaining, pageMS)`，负值归 0
- [x] 1.4 `compile.go` `injectCarry`：每跳以 `pageIdx-1` 页的盒为参照；`startMs=0`、`carry=true`、`elementId` 透传、MUST NOT 标 `isContinuation`
- [x] 1.5 `export_animation.go` `bundleClips` 透传 `carry`

## 2. 单测
- [x] 2.1 盒差数值：origin=center 时 `sx/sy=1.2`、`tx/ty=26/19`（手算，含 `O·(1-s)` 项）
- [x] 2.2 同盒不产补偿，且次页段数不增加
- [x] 2.3 链式两跳分别以上一跳的盒为参照，补偿量不相等
- [x] 2.4 5 种锚点（缺省/center/top/left/right/bottom）的补偿量
- [x] 2.5 `CarryDuration` 三条边界（上限、剩余不足、页长不足、已演完）
- [x] 2.6 `bundleClips` 透传契约：carry/elementId 保留，`ContinuationSource` 与空键不进清单

## 3. 门禁
- [x] 3.1 V19 新增「视觉矩形」计算（`box` 叠加 `translate·scale·origin`）
- [x] 3.2 V19.8 页缝两侧视觉矩形连续（含补偿）
- [x] 3.3 V19.9 补偿段终点回到恒等变换
- [x] 3.4 V19.10 包内必须存在补偿样本（防止夹具退回同盒后判据恒真）
- [x] 3.5 V19.5 改为排除 carry 后比较动效合成态
- [x] 3.6 源段配对跳过 carry 段

## 4. 夹具
- [x] 4.1 `share-baseline` 的单跳/链式延续层使用**不同盒**（k=0/1/2/3，位置与尺寸都变）
- [x] 4.2 夹具调用 `render.CarryProps` 与 `render.CarryDuration`，与编译器共用同一份数学

## 5. 规范与收尾
- [x] 5.1 `render-plan` ADDED「跨页延续的静态盒差在编译期补偿」
- [x] 5.2 `html-share-quality` MODIFIED：页缝门禁加入视觉矩形、补偿终点、补偿样本三条判据
- [x] 5.3 路线图补一行，写明边界（不插值内容、不做像素级验收）
- [x] 5.4 `go build/vet/test -p 1 ./...` + `npm run build` + `build:share` 无漂移
- [x] 5.5 V19 正向 60/60；负向三例（缺补偿 / 偏 10px / 不收敛）均退出码 1
- [x] 5.6 `openspec validate` 通过并归档
