# ADR-040：本地后端的成品发布走文件移交（rename），大文件不再读两遍

- 状态：已实现
- 日期：2026-11-09
- 相关：`docs/优化方案-2026-11-08.md` §P3-5；`internal/integrations/objectstore/port.go`（`FilePublisher`）；`local.go` / `registry.go` / `inventory.go`（`PutFile`）；`internal/app/export.go`（`publish`）

## 背景

导出的大文件成品（mp4 / web_project / html_bundle / html_single）都先落在临时文件上，
发布分两步（`internal/app/export.go`）：

```
fileExportOutput：Seek(0) → hashReader（读第 1 遍）→ Seek(0)
storeArtifact：objects.Put(key, file) → LocalFS.Put 里 io.Copy（读第 2 遍 + 写 1 遍）
```

第 1 遍读省不掉：对象键是 `{tenant}/{project}/artifact/artifact/{contentHash}.{ext}`，
`AssetID` 就是内容哈希，发布前必须先算出来。第 2 遍读 + 那一遍写是纯搬运：文件已经在
本机磁盘上了，本地后端只需要把它挪进对象根。方案原提法是「`io.TeeReader` + 同盘
`os.Rename`」；实际不需要 Tee——产物临时文件就是现成的 staging，哈希读不需要同时
tee 出第二份（那会把「写一遍」也付掉）。

实测（本机 SSD，128 MB 样本，`BenchmarkLocalPublish`）：

| 路径 | 耗时 | 折算 |
|---|---|---|
| 改前（哈希读 + Put 拷贝） | 195.2 ms/op | 687 MB/s |
| 改后（哈希读 + rename） | **79.1 ms/op** | 1698 MB/s（≈2.5×） |

省下的 116 ms/128 MB 就是那次「读一遍 + 写一遍」的搬运；500 MB 成片每次导出省约
0.45 s，SSD 写入量减半。

## 决策

1. **`objectstore.FilePublisher` 作为可选能力接口**：`PutFile(ctx, key, path, meta)`。
   契约：最终状态与 `Put` 完全一致（原子替换、0644、幂等覆盖）；成功后调用方不得再读
   `path`，对 `path` 的 `os.Remove` 必须在两种实现下都安全（rename 后 ENOENT 幂等、
   拷贝后正常删除）。
2. **只有 `LocalFS` 实现它**。两层转发保证生产接线（`Registry → inventoryStore →
   LocalFS`）能到达：Registry 按租户路由到后端的 `PutFile`；inventoryStore 在两条路
   （rename 直发 / reader 回退）成功后都登记——漏登记的对象会被周级孤儿扫描误删。
   两层的回退统一走 `putFileFallback`（打开文件交给 `Put`）。
3. **`LocalFS.PutFile`：chmod 0644 → 同盘 `os.Rename`；rename 失败（跨盘 EXDEV、目标
   被独占句柄占用）退回 `Put` 的「临时文件 + rename」拷贝路径**。rename 只动目录项，
   发布零字节搬运。
4. **app 侧 `storeArtifact` 抽出 `publish`**：`out.file != nil` 且存储实现了
   `FilePublisher` 时走 `PutFile`；否则（内存产物、S3、加密包装）走原 reader 路径，
   语义不变。
5. **加密包装（`encryptionStore`）故意不实现 `FilePublisher`**，并有测试钉死
   （`TestEncryptionStoreDoesNotPublishFiles`）：文件路径上是明文，实现它等于把明文
   rename 进对象根、绕过信封加密。加密租户靠调用方的类型断言失败回退 reader 路径。

## 明确不做

- **S3 不实现 `PutFile`**：上传本来就要把字节读出来，文件接口没有增益；断言失败后
  app 直接用已打开的 reader `Put`，比「后端内部再 open 一次」少一次路径往返。
- **不做哈希-on-write（写产物时顺手算哈希）**：能消掉最后一遍读，但只对 Go 写出的
  zip/html 生效——mp4 是 ffmpeg 写的，钩不进去。为三种格式 splitting 出第二条哈希
  路径，换来的是「同一产物有两种哈希来源」的维护成本；收益（省一遍顺序读，
  ~1.6 GB/s）在整条导出链里不显形。重估条件：导出吞吐成为瓶颈，或产物普遍上 GB。
- **不让 app 感知「产物临时文件应建在对象根同卷」**：跨盘时 rename 回退拷贝，
  行为与改前一致（读 2 遍写 1 遍），不劣化。若单机部署把 `TMPDIR` 与对象根放同一卷
  （默认部署就是），rename 自动生效。

## 后果

- 本地后端同盘发布：读 2 遍写 1 遍 → **读 1 遍写 0 遍**（剩下那遍是内容哈希）。
- `exportOutput.Close` 无需感知发布方式：rename 后 remove 旧路径是幂等空操作，
  拷贝回退后正常清理。
- 判据与变异：`TestExportPublishesFileArtifactByRename` 断言文件成品发布必走
  `PutFile` 且 reader 通道为 0（改回 reader 路径立刻红）；`TestLocalPutFileRenamesSameVolume`
  断言源路径被 rename 走（拷贝实现会留下源文件）；`TestLocalPutFileFallsBack…`、
  `TestInventoryPutFileRecordsBothBranches`、`TestRegistryPutFileRoutes…` 各守一层。
  变异 6 条全部杀掉；唯一例外是「漏 chmod」在 Windows 上不可观测（0600 与 0644 都是
  可写），已用 WSL 在 Linux 下复核必死，测试注释中记录了这一平台差异。
