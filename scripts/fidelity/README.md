# 出片保真检查（ADR-18）

回答一个问题：**渲染出来的视频，内容比源页少了什么没有？**

仓库里原先的样片是用色块占位的，画面里一个字形都没有。它适合验时间轴、转场、残影，
但对「文字丢没丢」完全不敏感 —— ADR-15 的空格缺陷和 ADR-17 的图层顺序缺陷都是
换成带真实文字的样片之后才暴露的。这套脚本就是那份样片的来源。

## 四步跑完

```bash
# 1) 造底图：字体从镜像里取，保证与线上同一份字形
docker create --name fsrc ppts/renderer:dev
docker cp fsrc:/usr/share/fonts/opentype/ppts /tmp/ppts-fonts && docker rm -f fsrc
python3 scripts/fidelity/gen_pages.py --fonts /tmp/ppts-fonts --out /tmp/fidelity

# 2) 编译成计划（底图会被拷进 job 目录，见 ADR-14）
go run ./scripts/fidelity/plan -in /tmp/fidelity -out /tmp/job

# 3) 提交给渲染服务（--assets-dir 指向 /tmp/job 的只读挂载）
curl -s -X POST --data-binary @/tmp/job/plan.json http://127.0.0.1:8090/v1/render
#    然后轮询 GET /v1/render/{jobId} 直到 succeeded，取回 segment_*.mp4

# 4) 比对末帧与源页
python3 scripts/fidelity/compare.py \
    --pages /tmp/fidelity/pages \
    --elements /tmp/fidelity/elements.json \
    --segments /tmp/job-out
```

## 判据

| 指标 | 阈值 | 说明 |
|---|---|---|
| 整页平均绝对差 | ≤ 2.0 | H.264 编码噪声在 0.5–1.3；到 2 以上多半是内容变了 |
| 差 >32 的像素占比 | < 1% | 结构性丢失会到几个百分点（ADR-17 是 5.8%） |
| 元素区域墨量比 | ≥ 0.8 | 文字被盖掉/淡掉时直接趋近 0 |

任何一条不过，脚本以非零退出。这三个阈值来自实测：5 页带文字样片在正确实现下是
平均差 0.56–1.23、差 >32 的像素 <0.35%、墨量比 ≥0.95。

## 两个容易踩的口径坑

- **取末帧**，不要取页中任意一帧：入场动画在页末才回到恒等，取中间帧会把动画状态算成差异。
- **墨量的基准色只从源图取**：两边各自取中位色时，渐变区域会得到不同基准，
  算出来的是基准漂移而不是像素差异（第一版因此虚报了 3.7 个百分点）。

## 想复现 ADR-17 那个缺陷

把 `gen_pages.py` 里 `page1` 的图层顺序改成「先 items 后 card」再跑一遍：
三行要点的墨量会从约 6% 掉到 0.00%，而任务状态仍是 `succeeded`、没有任何 warning。
