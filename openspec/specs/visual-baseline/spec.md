# visual-baseline Specification

## Purpose
定义成片（1080p 幻灯片视频）的视觉与运动基线：画布栅格、字阶、色板、版式原型、运动质感参数与观感度量。它是"M2 渲染什么"的输入契约，本身不含渲染实现；任何渲染器（MP4 通道、HTML 通道）MUST 从同一份令牌取值，MUST NOT 各写一套数值。

## Requirements

### Requirement: 画布与栅格唯一且自洽

系统 SHALL 只提供一套成片画布：`1920×1080`、16:9。栅格 MUST 为 12 列 × 8 行，列间距与行间距 MUST 相等（同一 `gutter`），四边留白 MUST 由 `marginX`/`marginY` 定义。

`Columns*colWidth + (Columns-1)*gutter + 2*marginX` MUST 恰好等于画布宽度；行方向同理。任何参数改动导致等式不成立 MUST 在启动自检中失败，MUST NOT 静默取整。

#### Scenario: 栅格等式成立

- **WHEN** 以默认令牌（marginX=96、marginY=72、gutter=24）计算 12×8 栅格
- **THEN** 列宽为 122px、行高为 96px，且横向与纵向的等式同时成立（1728=12×122+11×24、936=8×96+7×24）

#### Scenario: 参数被改坏

- **WHEN** 某处把 `gutter` 改为与 `marginX` 不匹配的值，使等式差 1px
- **THEN** 校验返回错误并指出差额，MUST NOT 通过向下取整"修好"

### Requirement: 字阶含正文下限

系统 SHALL 提供命名文字档位（`display`/`h1`/`h2`/`h3`/`lead`/`body`/`bodySm`/`caption`），每个档位 MUST 声明字号、行高倍数与字距。正文档位（`body`、`bodySm`）在 1080p 下的字号 MUST ≥ 32px。

`caption` 只允许用于页脚、来源与页码，MUST NOT 承载正文语义；其字号 MUST 单独登记为"装饰性档位"，并在度量中按非正文处理。

#### Scenario: 正文档位低于下限

- **WHEN** 有人把 `body` 字号改成 28px
- **THEN** 令牌自检失败，错误信息给出下限 32px 与违反的档位名

#### Scenario: 装饰档位不受正文下限约束

- **WHEN** `caption` 为 24px
- **THEN** 自检通过，但 `caption` MUST 被标记为非正文，度量时不得用它代表正文字号

### Requirement: 色板可被自身对比度规则验证

系统 SHALL 提供色板令牌，每项 MUST 声明用途（`ink`/`paper`/`accent`/`semantic`）。所有被声明为"正文可读"的前景/背景组合 MUST 满足 WCAG 2.1 对比度 ≥ 4.5:1；装饰元素（分隔线、色块、描边）MUST NOT 计入该契约。

对比度计算 MUST 使用 sRGB 相对亮度公式（含 gamma 反变换），MUST NOT 使用简单亮度平均。

#### Scenario: 白底主强调色

- **WHEN** 计算 `#4338CA` 与 `#FFFFFF` 的对比度
- **THEN** 结果 ≥ 4.5:1（实测约 7.9:1），该组合可登记为正文可读

#### Scenario: 灰字踩线

- **WHEN** 把正文色设为 `#94A3B8` 并放在 `#FFFFFF` 上
- **THEN** 对比度约 2.4:1，色板自检失败并给出实际值与阈值

### Requirement: 版式原型以栅格声明槽位

系统 SHALL 提供不少于 10 个版式原型，覆盖封面、章节卡、要点、图文、全幅图、大数字、引文、图表主角、对比、结尾。每个原型 MUST 声明 `id`、`purpose`（何时用）、`slots[]`；每个槽位 MUST 声明 `role`、列起点/跨列、行起点/跨行与对齐方式。

槽位 MUST 落在 `1..12` 列与 `1..8` 行内，同一原型内槽位 MUST NOT 重叠。每个原型 MUST 至少含一个文本槽位，且 MUST 声明一个主标题或主陈述槽位。

#### Scenario: 槽位越界

- **WHEN** 某原型的槽位声明 `col=11, colSpan=3`
- **THEN** 校验失败并指出该槽位右边界为 13，超出 12 列

#### Scenario: 槽位重叠

- **WHEN** 两个槽位同时占用第 3 行第 1–6 列
- **THEN** 校验失败并给出两个槽位 id

### Requirement: 运动质感参数集中且可查询

系统 SHALL 提供运动质感档案，MUST 至少覆盖：stagger（组内依次出现的间隔区间）、anticipation（预备反向位移时长占比与幅度）、overshoot（回弹阻尼区间与最大幅度）、hold（关键动作后的静置下限）、速度分层（大/中/小元素的时长区间）、beat（每页节拍数区间与单拍时长区间）、命名缓动曲线。

参数值 MUST 与设计方案 §19.4 的参考值一致；偏离 MUST 在档案注释中写明原因。stagger 序列 MUST 由纯函数生成（支持顺序与中心扩散两种模式），同一输入 MUST 产生同一序列。

#### Scenario: 组内错峰

- **WHEN** 请求 4 个元素、起始 0ms、stagger 75ms、顺序模式
- **THEN** 返回 `[0, 75, 150, 225]`，且重复调用结果一致

#### Scenario: 中心扩散

- **WHEN** 请求 5 个元素、中心扩散模式，间隔 60ms
- **THEN** 中间元素最先（偏移 0），两侧依次 +60ms，边界元素偏移为 120ms

### Requirement: 观感度量可执行且分级

系统 SHALL 提供对单页事实的度量入口，输入为纯数值（页时长、元素数、最小正文字号、占用面积比、clip 数、节拍数），输出 `Finding` 列表，每条 MUST 带规则编号、严重级别、实测值、阈值与修正建议。

规则 MUST 至少包含：正文最小字号（error）、文本对比度（error）、主要元素数 7±2（warn）、留白比例下限（warn）、节奏密度（warn 与 error 两级：>3 clips/s 警告、>6 clips/s 阻断）、节拍数 3–5（warn）。

度量 MUST 是纯函数：不读时钟、不读文件、不访问网络；同一输入 MUST 得到同一报告。

#### Scenario: 元素过密

- **WHEN** 一页 6s、主要元素 12 个、clip 24 条（4 clips/s）
- **THEN** 报告含元素数 warn（超出 9）与节奏密度 warn（超过 3/s），但不含 error，页面仍可通过

#### Scenario: 字号不达标

- **WHEN** 最小正文字号为 28px
- **THEN** 报告含 error 级 finding，规则编号为字号规则，且 `Report.OK()` 返回 false

### Requirement: 资产清单必须声明许可

凡进入成片的图像/图标/插画资产 MUST 在清单中登记 `id`、`kind`、`version`、`license`、`file`。`license` MUST 非空且 MUST 取自允许集合（自产、OFL、CC0、CC-BY、商用授权）；缺失或未知 MUST 被拒绝。

标注为 CC-BY 的资产 MUST 同时提供 `attribution`，否则 MUST 被拒绝——许可要求的署名不能在导出时再补。

#### Scenario: 缺少许可

- **WHEN** 登记一个 `license` 为空的图标
- **THEN** 校验失败，错误指出该资产 id 与缺失字段

#### Scenario: CC-BY 缺署名

- **WHEN** 登记 `license=CC-BY` 但未提供 `attribution`
- **THEN** 校验失败并说明"CC-BY 必须署名"

### Requirement: 预览页可离线打开

系统 SHALL 提供一个预览生成器，把令牌、版式原型与缓动曲线渲染为单个 HTML 文件。该文件 MUST 自包含（内联样式与脚本），MUST NOT 引用任何外部资源（字体、CDN、图片 URL），在 `file://` 下 MUST 可直接打开并正确显示。

预览页 MUST 展示每个版式的槽位分布与字号占比，使"版式是否合理"可被非工程师判断。

#### Scenario: 双击打开

- **WHEN** 在无网络环境下双击生成的预览文件
- **THEN** 页面完整显示 10 个版式、字阶样例与色板对比度，且无空白区域或控制台资源错误
