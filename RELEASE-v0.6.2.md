# PPTS 0.6.2

页级片种不被一键冲掉；免 Chrome 纱/框贴近预览；HTML 跟 MP4 同一套 look 钟；导出可选导演加强。

已有成品需重新导出才会换像素和 HTML 钟。控制台请硬刷新。

## 用户可见

- **页级片种**：逐页把注视改成显现（或反过来）并保存后，一键成片不会冲掉这一页。跳过原因显示「本页片种已单独设置」。
- **纱和黄框**：免 Chrome 的注视仍帧现在有 8px 羽化和圆角框，观感跟预览更接近。
- **HTML 成品**：新导出的分享包按 MP4 的 look 窗定格，不再在每个注视里多播 240ms 淡入。旧包行为不变。
- **导演加强**：一键成片和导出对话框里可选。默认关。勾选后对已经排出的拍表再问一次模型；失败仍用原来的拍表。封面等拍表不够的页，不勾选导出也会补救。

## 运维

- 日志：`export wall clock`（total / assets / render / concat / publish / other 等）。
- 指标：`ppts_export_duration_ms_total`、`ppts_export_stage_duration_ms_total`。
- 页图、配音、渲染片段最多 4 路并发拉取。
- 片段键增加 `paint=feather`，旧硬边纱不会从缓存命中。
- 备份：`/opt/ppts/bin/ppts.backup-before-v0.6.2`、`ppts-renderer.backup-before-v0.6.2`。
- 未改：切近仍关；默认片种仍是注视；默认风格仍是商务稳重。

## 回滚

还原上述两个备份后重启 `ppts-api` `ppts-worker` `ppts-renderer`。已保存的 `formSource=page` 在旧二进制上会被忽略。
