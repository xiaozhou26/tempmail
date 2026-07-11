# tempmail — 自建临时邮箱（域名邮箱）后端

基于 **Go + Gin** 的纯后端 API 服务，配合 **Cloudflare Email Routing** 收取临时域名邮箱邮件。无前端、无数据库中间件（内嵌 SQLite）。

支持两种收信方式：

- **Microsoft Graph API（推荐）**：通过 Graph 读取转发邮箱的邮件，个人 Outlook 账号也能稳定使用。
- **IMAP 轮询**：定时用 IMAP 连转发邮箱拉取未读邮件（支持 Gmail 应用专用密码、Outlook OAuth2 等）。

两种模式都不消耗 Cloudflare Worker 次数——Cloudflare Email Routing 的「转发到目标邮箱」是免费路由功能，不经过 Worker。

## 工作原理

```
发件人 ──SMTP──> Cloudflare MX ──> Email Routing(catch-all *@yourdomain)
                                          │
                                          │ 转发到真实邮箱（免费，不触发 Worker）
                                          ▼
                              中转邮箱 (Gmail / Outlook / 自建)
                                          ▲
                                          │ Graph API / IMAP 轮询拉取
                              ┌───────────┴────────────┐
                              │  Go + Gin 后端 (本项目) │
                              │  - 存 SQLite            │
                              │  - 提供查询/删除 REST API │
                              └────────────────────────┘
```

- **Cloudflare 侧**：开启 Email Routing，把 `*@yourdomain` 配为 catch-all **转发到目标邮箱**（不是 Send t Worker）。
- **中转邮箱**：任意支持 Graph/IMAP 的邮箱（个人 Outlook 推荐 Graph 模式）。
- **Go 后端**：定时读取中转邮箱新邮件，按收件地址路由到对应临时邮箱并入库。

## 项目结构

```
.
├── main.go                 # 入口：路由 + 定时清理 + 收信 poller
├── config/config.go        # 环境变量配置 + refresh_token 轮换写回
├── models/models.go        # GORM 数据模型 (Mailbox / Message)
├── storage/db.go           # SQLite 初始化与自动迁移
├── middleware/auth.go      # API Key 与 Webhook Secret 鉴权
├── handlers/
│   ├── email.go            # 邮箱创建/查询/删除
│   ├── message.go          # 邮件查询/删除（detail 含 raw）
│   ├── webhook.go          # 可选 webhook 入口（默认禁用）
│   ├── store.go            # IMAP/webhook 通用：解析 RFC822 并存储
│   └── graph_store.go      # Graph 模式：把 Graph JSON 邮件存库
├── graph/poller.go         # Graph API 轮询器（推荐）
├── imap/poller.go          # IMAP 轮询器（备选）
├── tools/get_graph_token.py # 获取 Graph refresh_token 的脱敏脚本
├── Dockerfile              # 多阶段构建静态镜像
├── build.sh                # 本地交叉编译
├── .github/workflows/release.yml  # 打 tag 自动发版（二进制 + GHCR 镜像）
└── .env.example
```

## 前置条件

1. 一个托管在 **Cloudflare** 的域名（NS 已切到 Cloudflare）。
2. 一个中转邮箱：
   - **Outlook 个人账号**：推荐 Graph 模式（IMAP OAuth2 常报 `User is authenticated but not connected`）。
   - **Gmail**：用 IMAP + 应用专用密码。

## 一、配置 Cloudflare Email Routing（转发，非 Worker）

1. Cloudflare 控制台 → 选择域名 → **Email > Email Routing**，按提示添加 MX/TXT 记录。
2. **Email Routing > Routes** → 新建 catch-all 规则：
   - **Catch-all address** → Action 选 **Send to an email** → 填中转邮箱地址。
   - ⚠️ 不要选 "Send to a Worker"，那样会消耗 Worker 次数。
3. 去中转邮箱确认 Cloudflare 发来的验证邮件。

## 二、运行 Go 后端

```bash
cp .env.example .env
# 编辑 .env：填 MAIL_DOMAIN / API_KEY / 收信模式凭据
go build -o tempmail .
./tempmail
```

启动后日志应显示 poller 已启动，例如 Graph 模式：

```
graph poller started: your-account@outlook.com every 60s
```

建议用 nginx/Caddy 反代到 HTTPS 对外提供 API。

### 交叉编译 Linux 二进制

SQLite 用纯 Go 驱动，无需 CGO，直接交叉编译：

```bash
bash build.sh                                          # dist/tempmail-linux-amd64
VERSION=v1.0.0 PLATFORMS="linux/amd64 linux/arm64" bash build.sh
```

把二进制和 `.env` 上传到服务器即可运行。

### Docker

镜像由 GitHub Actions 推送到 GHCR（`ghcr.io/xiaozhou26/tempmail`）。也可本地构建：

```bash
docker build -t tempmail:local --build-arg VERSION=v1.0.0 .
```

运行：

```bash
# 先准备好 .env（至少 MAIL_DOMAIN / API_KEY / 收信凭据）
docker run -d --name tempmail   -p 8080:8080   -v tempmail-data:/data   --env-file .env   ghcr.io/xiaozhou26/tempmail:1.0.0
```

- 监听 `LISTEN_ADDR`（默认 `:8080`），SQLite 默认写在 `/data/tempmail.db`（已挂 volume）。
- Graph/IMAP 若会轮换 `refresh_token`，容器内默认无法持久写回 `.env`；请把 token 当环境变量传入，或挂载可写的 `.env` 到工作目录 `/app/.env`。
- 健康检查：`GET /healthz` → `{"ok":true,"version":"..."}`。

### 发布 1.0.0（GitHub Actions）

推送语义化 tag 会触发 [`.github/workflows/release.yml`](.github/workflows/release.yml)：

1. 交叉编译静态二进制：`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`
2. 构建并推送多架构 Docker 镜像到 GHCR：`linux/amd64` + `linux/arm64`（tag：`1.0.0` / `1.0` / `1` / `latest`）
3. 创建 GitHub Release，附带二进制与 `checksums.txt`

```bash
git tag v1.0.0
git push origin v1.0.0
```

也可在 Actions 页对 `Release` workflow 做 **Run workflow**，并填入 tag（如 `v1.0.0`）。

首次推送后 GHCR 包可能是 private，到仓库 **Packages** 里改成 Public 即可匿名拉取。

## 三、API 使用

所有管理接口需带 API Key，两种方式任选：

- `Authorization: Bearer <API_KEY>`
- `X-API-Key: <API_KEY>`

详细接口说明见 **[api.md](api.md)**。快速示例：

```bash
# 创建临时邮箱
curl -X POST https://api.yourdomain.com/api/mailboxes \
  -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  -d '{"name":"alice","ttl_hours":12}'

# 列出某邮箱的邮件
curl -H "X-API-Key: $API_KEY" \
  https://api.yourdomain.com/api/mailboxes/alice@yourdomain.com/messages
```

## 收信延迟（速度）

默认已针对「尽快看到验证码邮件」做了优化：

| 模式 | 默认间隔 | 加速手段 |
|------|----------|----------|
| **Graph** | 10s 起 | 有新邮件时保持短间隔；整页 50 封时立即连拉；空闲时退避到最多 60s |
| **IMAP** | 15s 起 | **连接复用**（不每次 TLS 握手）；支持 **IMAP IDLE** 时服务器推送唤醒（秒级）；有未读时立即排空；空闲退避 |

可在 `.env` 调更激进（注意邮箱提供商限流）：

```env
GRAPH_POLL_INTERVAL_SEC=5
IMAP_POLL_INTERVAL_SEC=5
```

> Cloudflare Email Routing 本身的转发延迟通常是主要下限（数秒到数十秒），poller 只能优化「转发到中转邮箱之后」这一段。

## 收信模式

### Graph 模式（推荐，个人 Outlook 账号）

```env
GRAPH_ENABLED=true
GRAPH_CLIENT_ID=your-azure-app-client-id
GRAPH_CLIENT_SECRET=your-azure-app-secret
GRAPH_TENANT_ID=common
GRAPH_ACCOUNT=your-relay@outlook.com
GRAPH_REFRESH_TOKEN=your-refresh-token
GRAPH_TOKEN_SCOPE=offline_access Mail.Read Mail.ReadWrite User.Read
```

需要 Azure 应用已授予 **Microsoft Graph 委托权限**：`Mail.Read`、`Mail.ReadWrite`、`offline_access`、`User.Read`，并完成管理员同意。

获取 `refresh_token` 用脱敏脚本：

```bash
python tools/get_graph_token.py
```

⚠️ **Microsoft refresh_token 每次换 access_token 都会轮换**，旧 token 立即失效。项目运行时会自动把新 token 写回 `.env`，所以**不要同时跑多个实例**，否则会互相覆盖。

### IMAP 模式（Gmail 等）

```env
IMAP_HOST=imap.gmail.com
IMAP_PORT=993
IMAP_USER=your-relay@gmail.com
IMAP_PASS=your-app-password
IMAP_MAILBOX=INBOX
IMAP_TLS=true
```

### IMAP + Outlook OAuth2（XOAUTH2）

```env
IMAP_AUTH_MODE=oauth2
IMAP_CLIENT_ID=your-client-id
IMAP_TENANT_ID=consumers
IMAP_REFRESH_TOKEN=your-refresh-token
IMAP_TOKEN_SCOPE=https://outlook.office.com/IMAP.AccessAsUser.All offline_access
```

> 个人 Outlook 账号的 IMAP XOAUTH2 常报 `User is authenticated but not connected`，这种账号请改用 Graph 模式。

## 配置项（.env）

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `MAIL_DOMAIN` | 域名邮箱的域名 | example.com（必填） |
| `API_KEY` | 管理 API 鉴权密钥 | （必填） |
| `GRAPH_ENABLED` | 启用 Graph 模式 | `false` |
| `GRAPH_CLIENT_ID` | Azure 应用 client_id | （Graph 模式必填） |
| `GRAPH_CLIENT_SECRET` | Azure 应用 secret | （Web 应用必填） |
| `GRAPH_TENANT_ID` | 租户 | `common` |
| `GRAPH_ACCOUNT` | 中转邮箱地址 | （Graph 模式必填） |
| `GRAPH_REFRESH_TOKEN` | Graph refresh token | （Graph 模式必填） |
| `GRAPH_TOKEN_SCOPE` | Graph scope | `https://graph.microsoft.com/.default` |
| `GRAPH_POLL_INTERVAL_SEC` | Graph 基础轮询间隔（秒，有自适应退避） | `10` |
| `IMAP_HOST` | 中转邮箱 IMAP 主机 | （IMAP 模式必填） |
| `IMAP_PORT` | IMAP 端口 | `993` |
| `IMAP_USER` | 中转邮箱账号 | （IMAP 模式必填） |
| `IMAP_PASS` | 中转邮箱密码/应用专用密码 | （plain 模式必填） |
| `IMAP_MAILBOX` | 拉取的邮箱文件夹 | `INBOX` |
| `IMAP_TLS` | 是否使用 TLS | `true` |
| `IMAP_POLL_INTERVAL_SEC` | IMAP 基础轮询间隔（秒；支持 IDLE 时近实时） | `15` |
| `IMAP_AUTH_MODE` | `plain` 或 `oauth2` | `plain` |
| `WEBHOOK_SECRET` | 可选 webhook 共享密钥 | （留空=禁用） |
| `LISTEN_ADDR` | 监听地址 | `:8080` |
| `DB_PATH` | SQLite 路径 | `./data/tempmail.db`（Docker 默认 `/data/tempmail.db`） |
| `DEFAULT_TTL_HOURS` | 新建邮箱存活时长（小时） | `24` |
| `CLEANUP_INTERVAL_MIN` | 过期清理间隔（分钟） | `30` |

> Graph 模式启用时，IMAP 配置被忽略。

## 数据迁移到 MySQL/PostgreSQL

本项目用 SQLite 是为了零依赖。如需更换，把 `storage/db.go` 中的驱动换成：

- PostgreSQL：`gorm.io/driver/postgres`
- MySQL：`gorm.io/driver/mysql`

模型与 handler 无需改动。
## 社区
友情链接：[LINUX DO](https://linux.do)

## 后台运行（systemd 示例）

```ini
# /etc/systemd/system/tempmail.service
[Unit]
Description=tempmail
After=network.target

[Service]
WorkingDirectory=/opt/tempmail
EnvironmentFile=/opt/tempmail/.env
ExecStart=/opt/tempmail/tempmail
Restart=always

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable --now tempmail
```
