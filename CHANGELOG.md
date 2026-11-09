# 变更说明

## v1.0.0

首个稳定版本。从更早的构建升级时，先跑 `ppts migrate`，再核对下面四条。

### 行为变化

- 上传与解析共用 100 MiB（`PPTS_MAX_UPLOAD_BYTES=104857600`，页数 `PPTS_MAX_PARSE_PAGES=100`）。此前 API 放到 512 MiB，解析阶段才失败。网页一直是 100 MiB，只改环境变量不会放宽浏览器检查。
- 布尔环境变量认 `1/true/yes/on` 与 `0/false/no/off`。已经写成 `PPTS_AUTH_DEV_HEADERS=1` 的环境，升级后会接受未签名身份头。生产保持不设置或 `false`。
- 终态任务默认保留 30 天（`PPTS_JOB_RETENTION_TTL=720h`）。到期删除会放开 `UNIQUE(tenant_id, idempotency_key, kind)`，同一个键可以再执行一次。本版本的契约是：幂等期等于保留期。同键要永久拒绝，就不能靠删任务行实现，本版本不另建永久幂等表。写成 `0` 或负数不会关闭保留，会退回 30 天。
- 导入报告与解析结果里的解析器版本是 `go-pptx-v2.0.3`。它跟随 `go.mod` 里 `github.com/F31/go-pptx/v2` 的 require，不是环境变量。

### 配置

全表在 `packaging/config.md`，由 `go generate ./internal/config/` 从登记表生成。`packaging/config.env.example` 是 PostgreSQL 单机安装模板（密钥 `CHANGE_ME`，TTS 为 `fake`）。什么都不设时进程走 SQLite，开发头关闭，没配指标 token 则拒绝抓取，没配配音供应商且没有模型网关时 worker 拒绝启动。

`server`、`worker`、`migrate` 遇到未登记的 `PPTS_*` 会打 WARN，日志只有键名。

### 已知限制

- 分组重名靠应用层预检查。库里还没有唯一索引，并发创建仍可能出现同名分组。
- 没装 ffmpeg 时，超长 WAV 混音轨会超过校验的 64 MiB 单条上限。装了 ffmpeg 后成片走 AAC。
- 发布构建必须 `GOWORK=off`。开发机的 `go.work` 挂着 `../go-pptx` 与 `./textnorm`，和发行包解析到的模块版本不是同一条路径。

### 打包

```bash
git tag v1.0.0
scripts/package.sh
```

未打标签时 `scripts/package.sh` 用提交哈希当版本号，只适合试包。
