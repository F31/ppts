# Spec Delta

## ADDED Requirements

### Requirement: 相机镜头的清晰度继承自特写路径

声明了 `camera` 的镜头 MUST 同时声明 `crop`（见 `html-share-bundle` 与 `render-plan` 的约束），
因此 MUST 复用「页内镜头切片」的 2x 重渲路径：有 `crop` 的页整页 300 DPI 重渲并裁出特写，相机
在其上运动。该特写的边缘梯度能量 MUST 满足与 300 DPI 直接裁切参照 ≥ 95% 的同款判据
（`animation-page-shots` 的 5.3）。

本需求 v1 MUST NOT 改变 300 DPI 触发逻辑。已知代价：相机 `scale` 远大于 1（特写上再大幅推近）
时，特写源自有限的 300 DPI 裁切放大倍率，极端放大仍可能糊——此为「清晰度 vs 体积」的已知权衡，
不在本需求范围。

#### Scenario: 相机镜头复用 2x 特写

- **WHEN** 某镜头同时声明 `camera` 与 `crop`
- **THEN** 其 `image` MUST 来自 300 DPI 重渲裁切的特写，边缘梯度能量 MUST ≥ 300 DPI 直接裁切
  参照的 95%

#### Scenario: 不引入额外 DPI 触发

- **WHEN** 导出带相机的包
- **THEN** 300 DPI 重渲触发规则 MUST 与引入本需求前一致（仅由 `crop` 触发），MUST NOT 因
  `camera` 单独改变重渲范围
