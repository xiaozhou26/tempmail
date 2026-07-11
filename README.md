# tempmail

自建**临时域名邮箱**后端（Go + Gin + SQLite）。

配合 **Cloudflare Email Routing** 的 catch-all 转发，把 `*@你的域名` 收到的邮件落到中转邮箱，再由本服务在**客户端查询时按需拉取**，通过 REST API 创建/查询/删除临时邮箱与邮件。

- 无前端、无外部数据库（内嵌 SQLite，纯 Go 驱动，**无需 CGO**）
- 不消耗 Cloudflare Worker 次数（用「转发到邮箱」，不是 Send to Worker）
- 收信：**IMAP**（Gmail / Firstmail 等）或 **Microsoft Graph**（个人 Outlook 推荐）

---

## 目录

1. [工作原理](#工作原理)
2. [快速开始（5 步）](#快速开始5-步)
3. [部署方式](#部署方式)
4. [收信配置](#收信配置)
5. [API 速查](#api-速查)
6. [配置项一览](#配置项一览)
7. [运维建议](#运维建议)
8. [项目结构](#项目结构)
9. [社区](#社区)

完整接口字段见 **[api.md](api.md)**。

---

## 工作原理

```text
发件人 ──SMTP──> Cloudflare MX
                      │
                      ▼
            Email Routing (catch-all *@yourdomain)
                      │  转发到中转邮箱（免费，不触发 Worker）
                      ▼
         中转邮箱（Firstmail / Gmail / Outlook …）
                      ▲
                      │  客户端 GET 邮件接口时按需 IMAP / Graph 拉取
                      │
              ┌───────┴────────┐
              │  tempmail 后端  │
              │  SQLite + REST  │
              └────────────────┘
```

要点：

| 组件 | 作用 |
|------|------|
| Cloudflare Email Routing | catch-all 转发到**一个真实中转邮箱** |
| 中转邮箱 | 实际收信；本服务只读它 |
| tempmail | 按需拉信 → 按收件地址路由到临时邮箱 → API 查询 |

**默认不是后台常驻轮询。** 只有客户端请求邮件相关接口时才会去中转箱拉一次（见 [按需拉取](#按需拉取on-demand)）。

---

## 快速开始（5 步）

### 1. 准备域名与中转邮箱

1. 域名 NS 指向 **Cloudflare**
2. 准备一个中转邮箱（任选）：
   - **Firstmail**：IMAP SSL `imap.firstmail.ltd:993` + 账号密码（最简单）
   - **Gmail**：IMAP + [应用专用密码](https://myaccount.google.com/apppasswords)
   - **个人 Outlook**：推荐 **Graph**（IMAP XOAUTH2 常不稳定）

### 2. 配置 Cloudflare Email Routing

1. 域名 → **Email → Email Routing**，按提示加 MX/TXT
2. **Routes → Catch-all**：
   - Action：**Send to an email**
   - 填你的中转邮箱
3. ⚠️ **不要**选 “Send to a Worker”
4. 在中转邮箱里点开 Cloudflare 验证邮件

### 3. 写配置

```bash
cp .env.example .env
```

最少需要：

```env
MAIL_DOMAIN=mail.example.com
API_KEY=用 openssl rand -hex 32 生成

# 例：Firstmail 中转
IMAP_PROVIDER=firstmail
IMAP_USER=relay@your-firstmail-domain.com
IMAP_PASS=your-password
```

> Graph 模式见 [收信配置](#收信配置)。`GRAPH_ENABLED=true` 时会忽略 IMAP。

### 4. 启动

```bash
go run .          # 开发
# 或
go build -o tempmail . && ./tempmail
```

期望日志（IMAP 示例）：

```text
imap on-demand fetch ready: relay@...@imap.firstmail.ltd:993 tls=true (triggered by GET messages)
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
# {"ok":true,"version":"dev"}
```

### 5. 调 API

```bash
export API_KEY='你的密钥'
export BASE=http://127.0.0.1:8080

# 创建临时邮箱
curl -s -X POST "$BASE/api/mailboxes" \
  -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  -d '{"ttl_hours":12}'

# 查邮件（会先按需拉中转箱，再返回）
curl -s -H "X-API-Key: $API_KEY" \
  "$BASE/api/mailboxes/<address>/messages"
```

生产环境请用 **HTTPS 反代**（Caddy / nginx）对外暴露。

---

## 部署方式

### A. 本机 / 服务器二进制

```bash
# 本机
go build -o tempmail .

# 交叉编译 Linux amd64（纯静态，无需 gcc）
bash build.sh
# → dist/tempmail-linux-amd64

VERSION=v1.0.0 PLATFORMS="linux/amd64 linux/arm64" bash build.sh
```

上传 `tempmail` + `.env` 到服务器后：

```bash
chmod +x tempmail
./tempmail
```

### B. Docker / GHCR

```bash
# 拉取（首次可能需把 GHCR 包设为 Public）
docker pull ghcr.io/xiaozhou26/tempmail:latest

# 或本地构建
docker build -t tempmail:local --build-arg VERSION=dev .

docker run -d --name tempmail \
  --restart unless-stopped \
  -p 8080:8080 \
  -v tempmail-data:/data \
  --env-file .env \
  ghcr.io/xiaozhou26/tempmail:latest
```

| 项 | 说明 |
|----|------|
| 监听 | 容器内默认 `:8080`（`LISTEN_ADDR`） |
| 数据库 | 默认 `/data/tempmail.db`（请挂 volume） |
| 健康检查 | `GET /healthz` |
| Graph token 写回 | 容器内默认写不进 `.env`；可把 token 当环境变量注入，或挂载可写 `/app/.env` |

### C. systemd

```ini
# /etc/systemd/system/tempmail.service
[Unit]
Description=tempmail temporary mailbox API
After=network.target

[Service]
WorkingDirectory=/opt/tempmail
EnvironmentFile=/opt/tempmail/.env
ExecStart=/opt/tempmail/tempmail
Restart=always
RestartSec=3
User=tempmail

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now tempmail
journalctl -u tempmail -f
```

### D. 发版（维护者）

推送 `v*` tag 会触发 GitHub Actions：多平台二进制 + GHCR 多架构镜像 + Release 附件。

```bash
git tag v1.0.1
git push origin v1.0.1
```

---

## 收信配置

### 按需拉取（on-demand）

| 触发接口 | 行为 |
|----------|------|
| `GET /api/mailboxes/:address/messages` | 先拉中转箱 → 返回该邮箱邮件 |
| `GET /api/mailboxes/:address` | 同上（响应含 `messages`） |
| `GET /api/messages/:id` | 先拉中转箱 → 返回详情 |

- 并发请求合并为一次拉取
- 约 1 秒内重复请求直接读库，避免打爆中转箱
- Cloudflare 转发延迟仍是主要下限（数秒～数十秒）
- 可选 Webhook 推送不受此限制

客户端等验证码时，建议 **1～3 秒** 轮询 messages 接口。

### 模式选择

| 中转邮箱 | 推荐模式 | 说明 |
|----------|----------|------|
| Firstmail | IMAP plain | `IMAP_PROVIDER=firstmail` |
| Gmail | IMAP plain | 应用专用密码 |
| 个人 Outlook | **Graph** | 避免 IMAP “authenticated but not connected” |
| 企业 Outlook | Graph 或 IMAP OAuth2 | 视租户策略 |

`GRAPH_ENABLED=true` 时**忽略**全部 IMAP 配置。

### Firstmail（IMAP）

```env
IMAP_PROVIDER=firstmail
IMAP_USER=your-account@your-firstmail-domain.com
IMAP_PASS=your-password
# 等价于：
# IMAP_HOST=imap.firstmail.ltd
# IMAP_PORT=993
# IMAP_TLS=true
# IMAP_AUTH_MODE=plain
# IMAP_MAILBOX=INBOX
```

Cloudflare catch-all 请转发到**同一个** Firstmail 地址。

### Gmail（IMAP）

```env
IMAP_HOST=imap.gmail.com
IMAP_PORT=993
IMAP_TLS=true
IMAP_AUTH_MODE=plain
IMAP_USER=your-relay@gmail.com
IMAP_PASS=your-app-password
IMAP_MAILBOX=INBOX
```

### Microsoft Graph（个人 Outlook）

```env
GRAPH_ENABLED=true
GRAPH_CLIENT_ID=your-azure-app-client-id
GRAPH_CLIENT_SECRET=your-azure-app-secret
GRAPH_TENANT_ID=common
GRAPH_ACCOUNT=your-relay@outlook.com
GRAPH_REFRESH_TOKEN=your-refresh-token
GRAPH_TOKEN_SCOPE=offline_access Mail.Read Mail.ReadWrite User.Read
```

1. Azure 应用授予委托权限：`Mail.Read`、`Mail.ReadWrite`、`offline_access`、`User.Read`，并管理员同意  
2. 用仓库脚本拿 token：`python tools/get_graph_token.py`  
3. ⚠️ **refresh_token 会轮换**，进程会写回 `.env`；**不要多实例并发**，否则互相顶掉 token  

### IMAP + Outlook OAuth2（可选）

```env
IMAP_HOST=outlook.office365.com
IMAP_PORT=993
IMAP_TLS=true
IMAP_AUTH_MODE=oauth2
IMAP_USER=your@outlook.com
IMAP_CLIENT_ID=...
IMAP_TENANT_ID=consumers
IMAP_REFRESH_TOKEN=...
IMAP_TOKEN_SCOPE=https://outlook.office.com/IMAP.AccessAsUser.All offline_access
```

个人账号若报 `User is authenticated but not connected`，请改用 Graph。

### Webhook（可选）

配置 `WEBHOOK_SECRET` 后启用 `POST /api/webhook/email`（需 `X-Webhook-Secret`）。  
适合 Email Worker 推送（会耗 Worker）；与 IMAP/Graph 可并存。

---

## API 速查

鉴权（二选一）：

```http
Authorization: Bearer <API_KEY>
X-API-Key: <API_KEY>
```

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| GET | `/healthz` | 无 | 健康检查 + `version` |
| POST | `/api/mailboxes` | Key | 创建临时邮箱 |
| GET | `/api/mailboxes` | Key | 列出邮箱 |
| GET | `/api/mailboxes/:address` | Key | 详情 + 邮件（会按需拉信） |
| DELETE | `/api/mailboxes/:address` | Key | 删邮箱及邮件 |
| GET | `/api/mailboxes/:address/messages` | Key | 邮件列表（会按需拉信） |
| GET | `/api/messages/:id` | Key | 邮件详情（可含 `raw`） |
| DELETE | `/api/messages/:id` | Key | 删单封 |
| POST | `/api/webhook/email` | Webhook | 可选推送 |

**等验证码最小流程：**

```bash
# 1) 创建
ADDR=$(curl -s -X POST "$BASE/api/mailboxes" \
  -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  -d '{"ttl_hours":1}' | jq -r .address)

# 2) 轮询（建议 1～3s）
curl -s -H "X-API-Key: $API_KEY" \
  "$BASE/api/mailboxes/$ADDR/messages"
```

字段与错误码详见 **[api.md](api.md)**。

---

## 配置项一览

从 `.env.example` 复制。也可用环境变量（容器 / systemd）。

### 必填

| 变量 | 说明 |
|------|------|
| `MAIL_DOMAIN` | 临时邮箱域名（与 Cloudflare 一致） |
| `API_KEY` | 管理 API 密钥 |

以及：**完整 IMAP 配置**（或 `IMAP_PROVIDER` + 账号密码），或 **Graph 全套**，或仅 `WEBHOOK_SECRET`（webhook-only）。

### 常用可选

| 变量 | 默认 | 说明 |
|------|------|------|
| `LISTEN_ADDR` | `:8080` | HTTP 监听 |
| `DB_PATH` | `./data/tempmail.db` | SQLite 路径（Docker 用 `/data/tempmail.db`） |
| `DEFAULT_TTL_HOURS` | `24` | 新建邮箱默认存活 |
| `CLEANUP_INTERVAL_MIN` | `30` | 过期清理周期（分钟） |
| `WEBHOOK_SECRET` | 空 | 非空则启用 webhook |

### IMAP

| 变量 | 默认 | 说明 |
|------|------|------|
| `IMAP_PROVIDER` | 空 | `firstmail` / `gmail` / `outlook` 预设 Host/Port/TLS |
| `IMAP_HOST` | 空 | 主机（或由 PROVIDER 填充） |
| `IMAP_PORT` | `993` | 端口 |
| `IMAP_USER` | 空 | 登录名（Firstmail 填完整邮箱） |
| `IMAP_PASS` | 空 | 密码 / 应用专用密码 |
| `IMAP_MAILBOX` | `INBOX` | 文件夹 |
| `IMAP_TLS` | `true` | 隐式 TLS（993） |
| `IMAP_STARTTLS` | `false` | 明文 + STARTTLS（143） |
| `IMAP_TLS_INSECURE` | `false` | 跳过证书校验（仅调试） |
| `IMAP_AUTH_MODE` | `plain` | `plain` 或 `oauth2` |
| `IMAP_CLIENT_ID` 等 |  | OAuth2 时需要 |

### Graph

| 变量 | 默认 | 说明 |
|------|------|------|
| `GRAPH_ENABLED` | `false` | `true` 启用并忽略 IMAP |
| `GRAPH_CLIENT_ID` |  | Azure 应用 |
| `GRAPH_CLIENT_SECRET` |  | Web 应用密钥 |
| `GRAPH_TENANT_ID` | `common` | 租户 |
| `GRAPH_ACCOUNT` |  | 中转邮箱地址 |
| `GRAPH_REFRESH_TOKEN` |  | 会轮换写回 `.env` |
| `GRAPH_TOKEN_SCOPE` | 见 example | 委托权限 scope |
| `GRAPH_MAIL_FOLDER` | 空 | 空 = 默认收件箱 |

---

## 运维建议

1. **API 走 HTTPS**；`API_KEY` 用 `openssl rand -hex 32`
2. **中转邮箱专用**，避免别的客户端同时抢未读（IMAP 以 `\Seen` 为进度）
3. **不要提交** `.env`、`*_tokens.txt`、refresh_token / client_secret
4. Graph **单实例**，避免 token 互顶
5. 数据在 SQLite 文件；备份即复制 `DB_PATH`
6. 需要 MySQL/PostgreSQL 时，改 `storage/db.go` 驱动即可，模型与 handler 可不动

---

## 项目结构

```text
.
├── main.go                 # 入口：路由、清理、按需收信装配
├── config/                 # 环境变量与 token 写回
├── models/                 # Mailbox / Message
├── storage/                # SQLite 打开与迁移
├── middleware/             # API Key / Webhook 鉴权
├── handlers/               # HTTP + 入库逻辑
├── ingest/                 # 按需拉取协调（OnDemand）
├── graph/                  # Microsoft Graph FetchOnce
├── imap/                   # IMAP FetchOnce（Firstmail/Gmail/…）
├── tools/                  # Graph token 脚本
├── Dockerfile
├── build.sh
├── .github/workflows/release.yml
├── .env.example
├── api.md                  # 完整 API 文档
└── README.md
```

开发命令见 [CLAUDE.md](CLAUDE.md)。

---

## 社区

友情链接：[LINUX DO](https://linux.do)
