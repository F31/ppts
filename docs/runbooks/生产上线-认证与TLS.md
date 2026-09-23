# 生产上线手册：公网注册 + 多租户 + TLS（第一批认证闭环）

> 适用：以 PostgreSQL 多租户模式对外发布，开放邮箱/手机自助注册。
> 本文覆盖第一批安全能力：邮箱验证、密码重置、登录限流、Cookie 会话、安全响应头、TLS 终止。

## 0. 前置

- 数据库：PostgreSQL（`PPTS_DB_DRIVER=postgres`）；迁移 0042 已提供（邮箱/手机分列、验证/重置令牌）。
- 邮件：一个可用的 SMTP 账号（如需真实收信）。未配置时验证/重置链接只打到服务端日志。
- TLS：应用本身只监听 HTTP，**必须**由反向代理/负载均衡终止 TLS。

## 1. 必配环境变量（/etc/ppts/config.env）

```ini
PPTS_DB_DRIVER=postgres
PPTS_DATABASE_URL=postgres://ppts_app:***@127.0.0.1:5432/ppts?sslmode=disable
PPTS_SCHEDULER_DATABASE_URL=postgres://ppts_scheduler:***@127.0.0.1:5432/ppts?sslmode=disable
PPTS_JWT_SECRET=<32B base64>
PPTS_PASSWORD_PEPPER=<32B base64>
PPTS_GATEWAY_AES_KEY_BASE64=<32B base64>

# 会话与安全
PPTS_JWT_TTL=12h
PPTS_PUBLIC_BASE_URL=https://app.example.com
PPTS_TRUST_PROXY=true           # 在受控反代之后
PPTS_COOKIE_SECURE=true         # 强制 Secure Cookie
PPTS_HSTS=true
PPTS_AUTH_DEV_HEADERS=false     # 必须 false

# 邮件
PPTS_MAIL_BACKEND=smtp
PPTS_SMTP_HOST=smtp.example.com
PPTS_SMTP_PORT=587
PPTS_SMTP_USER=no-reply@example.com
PPTS_SMTP_PASSWORD=***
PPTS_SMTP_FROM="PPTS <no-reply@example.com>"
PPTS_SMTP_TLS=starttls

# 可选：强制邮箱验证后才能登录
PPTS_REQUIRE_EMAIL_VERIFIED=false
```

> `PPTS_OBJECT_SECRET` / `PPTS_GATEWAY_AES_KEY_BASE64` 一旦丢失，历史对象与网关密钥将无法解密——**必须纳入密钥备份**，且不得在部署间变更。

## 2. 发布流程（迁移必须先于应用）

```bash
# 1) 备份数据库与对象存储
pg_dump "$PPTS_DATABASE_URL" > backup-$(date +%F).sql
# 对象存储：见 docs/runbooks/backup-restore-drill.md

# 2) 迁移（幂等；需 superuser DSN，单独一步、串行执行，勿并发）
sudo -u ppts PPTS_MIGRATE_DATABASE_URL="$PPTS_DATABASE_URL" /opt/ppts/bin/ppts migrate

# 3) 滚动/重启
sudo systemctl restart ppts-api ppts-worker

# 4) 冒烟
curl -sf https://app.example.com/healthz
curl -s  https://app.example.com/auth/config   # email_password=true, mail_configured=true
```

## 3. 反向代理（nginx 示例：TLS + 安全头 + 大文件上传）
```nginx
server {
  listen 443 ssl http2;
  server_name app.example.com;
  ssl_certificate     /etc/letsencrypt/live/app.example.com/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/app.example.com/privkey.pem;

  client_max_body_size 512m;          # PPTX 上传（应用侧不再有 1m 限制，但代理默认有）

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 300s;
  }
}
server { listen 80; server_name app.example.com; return 301 https://$host$request_uri; }
```

> 应用自身也会写 `X-Content-Type-Options` / `X-Frame-Options` / `CSP` 等安全头；反代重复设置无碍。
> 若 OIDC/埋点使用外部域名，记得把来源加进 `PPTS_CSP_CONNECT_EXTRA`。

## 4. 注册/登录行为（第一批）

- **账号自动识别**：含 `@` 视为邮箱，否则视为手机号；分别落 `users.email` / `users.phone`，全局唯一。
- **邮箱验证**：注册后发送 `https://app.example.com/verify-email?token=...` 链接（48h、单次使用）。
  未验证默认仅软提示；置 `PPTS_REQUIRE_EMAIL_VERIFIED=true` 可硬性要求。
- **密码重置**：`/login` →「忘记密码」→ 发送 `https://app.example.com/reset-password?token=...`（1h、单次使用）。
  重置成功会提升会话代次，**所有旧会话立即失效**。
- **手机号**：支持注册/登录，但**暂无短信通道**，手机号账号无法自助找回密码；如需重置，绑定邮箱后使用邮箱通道。
- **限流**（内存级，单实例）：注册 10/时/IP、登录 30/15min/IP、忘记密码 5/时/IP、重发验证 5/时/IP；
  同账号连续失败 5 次锁定 15 分钟。多实例部署需替换为共享存储（Redis）。

## 5. 上线检查清单

- [ ] `/auth/config` 返回 `email_password=true` 且（生产）`mail_configured=true`
- [ ] `PPTS_AUTH_DEV_HEADERS=false`；未配置 OIDC 时开发登录表单不出现
- [ ] 注册→收到验证邮件→点击链接→`email_verified_at` 非空
- [ ] 忘记密码→收到重置邮件→重置→旧密码失败、新密码成功、旧 Cookie 失效
- [ ] 连续输错密码触发锁定；高频注册触发 429
- [ ] 响应头含 `Content-Security-Policy`、`X-Frame-Options: DENY`、HSTS（TLS 下）
- [ ] Cookie 为 `HttpOnly; Secure; SameSite=Lax`
- [ ] 迁移 0042 已应用（`ppts_schema_migrations` 含 `0043_accounts_phone_verification.sql`）

## 6. 已知后续项（非本批）

- 短信验证/找回（需短信服务商）；多实例共享限流（Redis）；运营商后台（跨租户查看/挂起/提额）；
  Prometheus 指标与告警；合规数据删除入口。

## 7. 消息服务（设置 → 消息服务）

配置入口：控制台「设置 → 消息服务」，两个 Tab：

- **发件箱服务配置**（优先实现）：SMTP 服务器/端口/用户名/密码/发件人/TLS 方式；支持「发送测试邮件」。
  密码以 AES-GCM 密文入库（依赖 `PPTS_GATEWAY_AES_KEY_BASE64`，AAD 绑定 tenant_id+channel）。
  - 作用范围（仅运营商可选）：**本租户** 或 **平台默认**。平台默认用于新用户注册/验证等无租户上下文的邮件。
- **短信网关配置**：保存服务商/接入点/签名/模板/AccessKey（当前仅保存，发送能力后续接入）。

解析优先级（认证邮件）：**租户配置 → 平台默认 → 进程级 env（PPTS_SMTP_*）→ 日志**。
因此：至少配置其一即可打通邮箱验证与密码重置。

数据库表：`message_channels`（migration 0044），按 `(tenant_id, channel)` 一行；平台默认行 tenant_id=零 UUID。
写入后最迟 30s 生效（解析带 TTL 缓存）。

安全提示：设置页仅 ADMIN 可访问；「平台默认」仅 `PPTS_OPERATOR_USER_IDS` 中的运营商可写。
