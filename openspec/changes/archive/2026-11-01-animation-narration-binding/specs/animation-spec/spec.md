# Spec Delta

## ADDED Requirements

### Requirement: 机器生成的绑定必须标注来源

当 `Clip.trigger` 由系统预填而非用户手动设置时，规格 MUST 记录该绑定的来源，取值 MUST 属于白名单：
`literal`（字面相似）/ `count`（数量词→编号组）/ `ordinal`（第N条→第N块）/ `spatial`（方位指代）/
`sibling`（同块带出）/ `title-prior`（标题先验）/ `structural`（结构推断）/ `manual`。

来源字段 MUST 以 `omitempty` 序列化：不含来源的规格 MUST 与引入本字段前逐字节一致，`planHash` MUST 不变。
来源 MUST NOT 影响求值——编译器、播放器、渲染器 MUST NOT 因来源不同而改变输出。

用户修改一条预填绑定的 `trigger` 后，该绑定的来源 MUST 变为 `manual`，MUST NOT 保留原规则名：
过期的来源比没有来源更糟，它会让「按来源筛选」筛出一批其实已被人工改过的条目。

#### Scenario: 既有规格字节不变

- **WHEN** 加载并重新保存一份不含来源标注的既有规格
- **THEN** 序列化结果 MUST 与引入本字段前逐字节一致，`planHash` MUST 相同

#### Scenario: 未知来源被拒绝

- **WHEN** 写入的 `triggerSource` 不在白名单内
- **THEN** 保存 MUST 失败并返回允许的取值列表，MUST NOT 静默丢弃该字段

#### Scenario: 用户手改后来源失效

- **WHEN** 用户修改了一条来源为 `sibling` 的预填绑定的触发方式
- **THEN** 该绑定的来源 MUST 变为 `manual`，按来源 `sibling` 筛选时 MUST NOT 再包含它
