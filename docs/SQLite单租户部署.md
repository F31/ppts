# SQLite 单租户部署（轻量化 / 个人使用）

> 适用：单机、单用户、无需外部数据库的个人或轻量场景。
> 同一二进制通过 `PPTS_DB_DRIVER` 切换：`sqlite`（默认）与 `postgres`（多租户全功能）。

---

## 一、快速开始（零依赖）

```bash
# 1) 取得二进制（发行包或自行构建）
#    go build -o ppts ./cmd/ppts

# 2) 起服务：默认即 SQLite 单租户，无需任何数据库配置
export PPTS_HTTP_ADDR=:8080
export PPTS_OBJECT_BACKEND=local
export PPTS_OBJECT_ROOT=/var/lib/ppts/objects
export PPTS_OBJECT_SECRET="$(head -c32 /dev/urandom | base64)"

./ppts server          # API + 内嵌前端；首次启动自动建库/建表/播种本地身份
./ppts worker          # 另开终端：解析/讲稿/配音/导出任务执行器
```

- 打开 `http://localhost:8080/` 即可使用，**无登录**（自动以本地租户/用户身份运行）。
- 数据库文件默认 `~/.ppts/ppts.db`；可用 `PPTS_DATABASE_URL` 指定绝对路径。

---

## 二、能力边界（单租户 profile）

| 能力 | SQLite 模式 | PostgreSQL 模式 |
|------|------------|----------------|
| 项目 / 讲稿 / 配音 / 导出 | ✅ | ✅ |
| 标签 / 分组 | ✅（本地单用户） | ✅（租户内共享） |
| 用户登录（邮箱/OIDC/JWT） | ❌ 无登录，固定本地身份 | ✅ |
| 多租户 / 成员 / 角色 | ❌ 单租户，本地用户恒 owner | ✅（RLS + 角色） |
| 协作者 / 私密分享链接 | ❌ 无协作者语义 | ✅ |
| 公开作品广场 | ❌（不挂载 auth/public 路由） | ✅ |
| 项目级 ACL（#96） | 恒 owner，无隔离需要 | ✅ |
| 配额 / 用量 | ✅（本地账本，默认不限量） | ✅ |
| 模型网关 / 发音词典 | ✅ | ✅ |
| 保留清理 / 审计归档 / 存储生命周期 | ❌ 后台循环不启用 | ✅ |
| BYOS / 租户导出 | ❌ 返回 `ErrNotSupported` | ✅ |
| 对象存储后端 | `local`（S3 需环境变量） | local / s3 / byos |

> 数据模型：SQLite schema 保留 `tenant_id` 列但恒为本地租户（`00000000-0000-0000-0000-000000000001`），
> 无 RLS/角色/SECURITY DEFINER 函数；隔离由应用层显式 `tenant_id` 过滤承担。

---

## 三、配置项

| 变量 | 说明 | 默认 |
|------|------|------|
| `PPTS_DB_DRIVER` | `sqlite`（默认）/ `postgres`；留空按 DSN 前缀推断 | `sqlite` |
| `PPTS_DATABASE_URL` | sqlite：数据库文件路径；postgres：连接串 | `~/.ppts/ppts.db` |
| `PPTS_HTTP_ADDR` | API 监听地址 | `:8080` |
| `PPTS_OBJECT_BACKEND` | `local` / `s3` | `local` |
| `PPTS_OBJECT_ROOT` | local 后端数据目录 | `/var/lib/ppts/objects` |
| `PPTS_OBJECT_SECRET` | 本地对象签名密钥（32 字节 base64） | 必填 |
| `PPTS_TTS_PROVIDER` | `fake`（占位）/ `siliconflow` | `fake` |
| `PPTS_GATEWAY_AES_KEY_BASE64` | 模型网关凭据加密密钥（可选） | 空 |

PostgreSQL 专属变量（`PPTS_SCHEDULER_DATABASE_URL`、`PPTS_JWT_SECRET`、`PPTS_PASSWORD_PEPPER`、
`PPTS_AUTH_DEV_HEADERS`、`PPTS_OIDC_*`）在 SQLite 模式被忽略。

---

## 四、数据与备份

```
~/.ppts/ppts.db            # 全部业务数据（WAL 模式；同目录有 -wal/-shm 临时文件）
$PPTS_OBJECT_ROOT/          # 对象存储（PPTX 源文件、页面 PNG、音频、成品）
```

备份：停止服务后复制 `ppts.db`（及其 `-wal`/`-shm`）与对象目录；或在运行时用
`sqlite3 ppts.db ".backup /path/backup.db"`（需 sqlite3 CLI）。

迁移到 PostgreSQL：用 PostgreSQL 模式重新部署后，通过 API 重新导入 PPTX；当前不提供
SQLite→PG 的数据搬迁工具。

---

## 五、切换到 PostgreSQL（多租户）

```bash
export PPTS_DB_DRIVER=postgres
export PPTS_DATABASE_URL='postgres://ppts_app:PW@127.0.0.1:5432/ppts?sslmode=disable'
export PPTS_SCHEDULER_DATABASE_URL='postgres://ppts_scheduler:PW@127.0.0.1:5432/ppts?sslmode=disable'
export PPTS_JWT_SECRET='...'
export PPTS_PASSWORD_PEPPER='...'
./ppts migrate   # superuser DSN：PPTS_MIGRATE_DATABASE_URL
./ppts server
```

多租户部署的安装脚本与 systemd 配置见 `packaging/`。

---

## 六、架构（分层解耦）

```
cmd/ppts/compose.go            组合根：按 PPTS_DB_DRIVER 构建 store 集
  └── internal/<domain>/
        store.go               接口 + 领域类型（驱动无关契约）
        postgres.go            pgx + RLS（tenant.Run 设置 app.tenant_id）
        sqlite.go              database/sql + 显式 tenant_id 过滤
internal/db/
  db.go                        Driver 配置解析（FromEnv）
  sqlite.go                    OpenSQLite / MigrateSQLite / EnsureLocalIdentity
migrations/
  *.sql                        PostgreSQL 迁移（RLS/角色/函数）
  sqlite/0001_init.sql         SQLite 单租户 schema（无 RLS/角色/函数）
```

- **驱动可选**：领域代码只依赖 store 接口，不感知驱动。
- **扩展点**：新增驱动只需实现同一组接口并在组合根注册，store 接口保持不变。
- **默认 SQLite**：单体开箱即用；团队/企业场景切 PostgreSQL 即得全功能。
