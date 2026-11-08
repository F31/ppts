# ADR-039：混音轨落临时文件，成品包组装不再整段驻留音频

- 状态：已实现
- 日期：2026-11-08
- 相关：`docs/优化方案-2026-11-08.md` §P3-4；`internal/share/bundle.go`（`bundleSink` / `packMix` / `writeMixTrack`）；`internal/media/m4a.go`（`EncodeM4AFile`）；ADR-032（混音轨 AAC）

## 背景

成品包（html_bundle / html_single）里唯一会**随时长线性增长**的条目是混音轨：24kHz 单声道
PCM16，5 分钟 14.4 MB、30 分钟 86 MB。它原来的组装方式是

```
media.WriteTimelineWAV(&bytes.Buffer, ...)  →  wav []byte
  → （有编码器时）EncodeM4A(wav) → m4a []byte
  → write(name, data)                          →  zip / 目录 / data URI
```

问题不在峰值本身，而在**同一份数据同时存在两份**：拼完的 WAV 在内存里，写 zip 时它还要
在压缩器里过一遍；`bytes.Buffer` 的倍增扩容又让"分配总量"再翻一倍。实测（5 分钟夹具）：

| 指标 | 改前 |
|---|---|
| 混音轨字节 | 14.4 MB |
| 一次 `Build` 总分配 | **32.0 MB**（≈2.2×，其中扩容占一半） |
| 30 分钟成片峰值工作集 | **298.5 MB** |

顺带一处更隐蔽的浪费：`media.MP4Encoder.EncodeM4A` 收 `[]byte`，内部又 `os.WriteFile` 落盘
给 ffmpeg——调用方先把它读进内存，编码器再写回磁盘，这一段来回完全是为了迁就接口签名。

## 决策

### 1. 混音轨写进临时文件，用 media 已有的 `io.WriteSeeker` 分支

`media.WriteTimelineWAV` 早就支持 seekable writer（按 clip 的精确偏移 `WriteAt`，全程只留
一个 8 KB 的静音块作缓冲）；`share` 之前没用上这条分支，只是因为它把 `*bytes.Buffer` 传了进去。
现在传 `*os.File`，media 一行未改就换来了流式组装。

### 2. 编码器接口改成"文件进、文件出"

```go
type M4AEncoder interface {
    EncodeM4AFile(ctx context.Context, srcPath, dstPath string) error
}
```

- **不保留 `[]byte` 版本给生产路径**：留一条就会有两种内存画像，而没人会去测第二条。
  `media.MP4Encoder.EncodeM4A([]byte)` 保留给"手上已有内存副本"的测试与门禁，内部转调新方法。
- **不返回产物大小**：由打包器自己 `stat`。产物是否真的落盘只有文件系统说了算，
  信任返回值等于多一处可漂移的口径（而"文件不存在但大小是 N"这种现场最难查）。
- 路径而不是 `io.Reader`：ffmpeg 需要 seekable 输出才能写 `-movflags +faststart`（ADR-032），
  输入侧也同一份临时目录最省事——把"要落盘"这件事收在编码器一侧，接口就不必假装能流。

### 3. sink 分两条路，文本条目禁止流式

`buildInto` 的唯一出口从 `func(name string, data []byte) error` 变成 `bundleSink`
（`writeEntry` / `writeStream`），zip、目录、单文件三种形态各一个实现。原因很直接：
同一条混音轨，写 zip 时应该从磁盘直接流进去，写单文件时必须 base64 进正文、只能收进内存。
把这条差别放进 sink，组装逻辑就不必知道自己在写给谁。

`bundleOut.file` 拒绝文本条目（`.html/.css/.js/.json/.vtt/.srt/.txt`）：自包含门禁
（V14）要先扫外链，而扫描需要完整字节。悄悄跳过校验比失败更糟——现场表现是"断网白屏"。

### 4. 临时目录的生命周期与使用点绑死

`writeMixTrack` 建目录、`defer` 删目录，返回给调用方的只有包内条目名，没有人需要"记得清理"。
这一条是被自己的用例抓出来的：第一版把目录建在 `packMix` 里、清理留给调用方，漏了成功路径，
一次 `go test` 就在 `%TEMP%` 留下 117 个目录、208 MB。

## 结果（本机实测）

| 场景 | 改前 | 改后 |
|---|---|---|
| 5 分钟夹具，`Build` 总分配（`-benchmem`） | 31.18 MB | **2.04 MB**（15×） |
| 5 分钟夹具，`Build` 耗时 | 19.8 ms | 33.1 ms（+13 ms） |
| 5 分钟夹具，判据用例的总分配 | 32.0 MB | **3.24 MB** |
| 30 分钟成片，峰值工作集（`io.Discard` 输出） | 298.5 MB | **129.1 MB**（−169 MB） |
| 30 分钟成片，构建耗时 | 9.19 s | 9.29 s（+1%） |

判据不看时间：`TestBuildDoesNotBufferWholeMix` 要求"打包一条 N 字节的混音轨，总分配量必须
小于 N"，夹具让多条时间轴段**复用同一份音频**（素材 480 KB、混音轨 14.4 MB），
于是分配量只可能来自混音轨本身。改回整段驻留立刻红（32.0 MB > 14.4 MB）。

另外两条守边界：`TestBuildCleansUpMixTempDir`（成功与降级两条路都不留临时目录）、
`TestBundleOutRejectsStreamedTextEntry`（流式通道不得吞掉外链扫描）、
`TestLocalBundleMatchesZipStructure` 新增"目录里的 mix 必须与 zip 里逐字节相同"
（只 stat 存在性的话，`dirSink` 建了文件却没写字节照样绿）。

变异验证 4 条全部杀掉：去掉文本守卫 / 目录流式不拷贝 / zip 流式不拷贝 / 不删临时目录。
（"整段驻留"这条不用造：旧实现本身就是它，32.0 MB 直接红。）

## 已知代价

- **时间换内存**：5 分钟样本多 13 ms，等于一次 14.4 MB 的临时文件写入 + 读回（单独实测
  10.3 ms）。成片越长，这一项绝对值越大，但相对整条导出链（渲染 + TTS + 打包）仍是噪声：
  30 分钟成片实测 9.19 s → 9.29 s。
- **无编码器时也走磁盘**：为了只有一条路径，没注入编码器（隐私模式没装 ffmpeg）同样先落盘
  再流进 zip。理论上可以用 `io.Pipe` 直连 zip 省掉这一次往返，代价是要把 media 的校验
  从写出里拆出来（流式一旦开始就无法退回"逐段音频"）。
- **单文件形态仍整段驻留**：base64 内联必须先有完整字节，这里没有省的余地（ADR-033 已记录）。
- **逐段音频（clips）仍在内存**：混音需要按 key 随机访问每一段，这是混音本身的约束，
  本 ADR 只去掉"再整段复制一份"。

## 重估条件

- 导出延迟开始被用户抱怨 → 无编码器路径改 `io.Pipe` + 拆出 media 的校验函数；
- 逐段音频成为下一个大头（长成片 clips 与 mix 同量级）→ 改成按段落盘、混音时逐段读；
- `VerifyBundle` 的 64 MiB 单条目上限（`internal/share/verify.go`）如果卡住真实的超长成片
  WAV（30 分钟 WAV 86 MB 会被它拒），要先决定"上限该抬"还是"超长必须走 AAC"——
  这是本 ADR 之外的一处独立口径，不在这次改动里改。
