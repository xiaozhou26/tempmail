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

- **Cloudflare 侧**：开启 Email Routing，把 `*@yourdomain` 配为 catch-all **转发到目标邮箱**（不是 Send to Worker）。
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
└── .env.example
```

## 前置条件

1. 一个托管在 **Cloudflare** 的域名（NS 已切到 Cloudflare）。
2. 一个中转邮箱：
   - **Outlook 个人账号**：推荐 Graph 模式（IMAP OAuth2 常报 `User is authenticated but not connected`）。
   - **Gmail**：用 IMAP + 应用专用密码。
3. Go 1.25+。SQLite 用纯 Go 驱动（modernc.org/sqlite），**无需 CGO/gcc**，可直接交叉编译静态二进制（见 `build.sh`）。

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
bash build.sh          # 输出 dist/tempmail-linux-amd64（静态二进制）
```

把 `dist/tempmail-linux-amd64` 和 `.env` 上传到服务器即可运行。

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
| `GRAPH_POLL_INTERVAL_SEC` | Graph 轮询间隔（秒） | `60` |
| `IMAP_HOST` | 中转邮箱 IMAP 主机 | （IMAP 模式必填） |
| `IMAP_PORT` | IMAP 端口 | `993` |
| `IMAP_USER` | 中转邮箱账号 | （IMAP 模式必填） |
| `IMAP_PASS` | 中转邮箱密码/应用专用密码 | （plain 模式必填） |
| `IMAP_MAILBOX` | 拉取的邮箱文件夹 | `INBOX` |
| `IMAP_TLS` | 是否使用 TLS | `true` |
| `IMAP_POLL_INTERVAL_SEC` | 轮询间隔（秒） | `60` |
| `IMAP_AUTH_MODE` | `plain` 或 `oauth2` | `plain` |
| `WEBHOOK_SECRET` | 可选 webhook 共享密钥 | （留空=禁用） |
| `LISTEN_ADDR` | 监听地址 | `:8080` |
| `DB_PATH` | SQLite 路径 | `./data/tempmail.db` |
| `DEFAULT_TTL_HOURS` | 新建邮箱存活时长（小时） | `24` |
| `CLEANUP_INTERVAL_MIN` | 过期清理间隔（分钟） | `30` |

> Graph 模式启用时，IMAP 配置被忽略。

## 数据迁移到 MySQL/PostgreSQL

本项目用 SQLite 是为了零依赖。如需更换，把 `storage/db.go` 中的驱动换成：

- PostgreSQL：`gorm.io/driver/postgres`
- MySQL：`gorm.io/driver/mysql`

模型与 handler 无需改动。

## 安全提示

- `API_KEY` 用 `openssl rand -hex 32` 生成强随机值。
- 中转邮箱建议专用，不要让其他邮件客户端同时读取（IMAP 模式只拉未读邮件）。
- **绝不要把 `.env`、`*_tokens.txt` 或任何 refresh_token / client_secret 提交到 git**——`.gitignore` 已默认排除。
- Graph 模式的 refresh_token 会轮换，项目会自动写回 `.env`；不要多实例并发。
- API 走 HTTPS 反代。

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
