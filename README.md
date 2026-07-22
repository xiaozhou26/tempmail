# tempmail

自建**临时域名邮箱**后端（Go + Gin + SQLite）。

内置 SMTP 服务器，**直接接收** `*@你的域名` 的邮件，无需依赖外部中转邮箱或 Cloudflare Email Routing。

- 无前端、无外部数据库（内嵌 SQLite，纯 Go 驱动，**无需 CGO**）
- 内置 SMTP 服务器，直接收信
- 可选：IMAP / Microsoft Graph 拉取中转邮箱（兼容旧架构）
- 可选：Webhook 推送

---

## 目录

1. [工作原理](#工作原理)
2. [快速开始（3 步）](#快速开始3-步)
3. [部署方式](#部署方式)
4. [DNS 配置](#dns-配置)
5. [API 速查](#api-速查)
6. [配置项一览](#配置项一览)
7. [运维建议](#运维建议)
8. [项目结构](#项目结构)
9. [社区](#社区)

完整接口字段见 **[api.md](api.md)**。

---

## 工作原理

```text
发件人 ──SMTP──> 本服务内置 SMTP 服务器（端口 25）
                       │
                       ▼  直接解析、存储
              ┌────────────────┐
              │  tempmail 后端  │
              │  SQLite + REST  │
              └────────────────┘
```

要点：

| 组件 | 作用 |
|------|------|
| 内置 SMTP 服务器 | 直接接收 `*@MAIL_DOMAIN` 的邮件 |
| tempmail REST API | 创建邮箱、查询/删除邮件 |
| SQLite | 本地存储，无需外部数据库 |

**不再需要 Cloudflare Email Routing 或外部中转邮箱。**

---

## 快速开始（3 步）

### 1. 配置

```bash
cp .env.example .env
```

最少需要：

```env
MAIL_DOMAIN=mail.example.com
API_KEY=用 openssl rand -hex 32 生成
SMTP_ENABLED=true
```

### 2. 配置 DNS

在你的域名 DNS 中添加：

| 类型 | 名称 | 值 | 优先级 |
|------|------|----|--------|
| MX | `@` | `mail.example.com` | 10 |
| A | `mail` | `你的服务器IP` | - |
| TXT | `@` | `v=spf1 mx a ~all` | - |

确保服务器开放 **25 端口**。

### 3. 启动

```bash
go run .          # 开发
# 或
go build -o tempmail . && ./tempmail
```

期望日志：

```text
starting tempmail dev for domain "mail.example.com" on :8080
smtp server enabled on :25 (hostname=mail.example.com, accepts mail for @mail.example.com)
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
# {"ok":true,"version":"dev"}
```

### 调 API

```bash
export API_KEY='你的密钥'
export BASE=http://127.0.0.1:8080

# 创建临时邮箱
curl -s -X POST "$BASE/api/mailboxes" \
  -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  -d '{"ttl_hours":12}'

# 查邮件
curl -s -H "X-API-Key: $API_KEY" \
  "$BASE/api/mailboxes/<address>/messages"
```

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
# 拉取
docker pull ghcr.io/xiaozhou26/tempmail:latest

# 或本地构建
docker build -t tempmail:local --build-arg VERSION=dev .

docker run -d --name tempmail \
  --restart unless-stopped \
  -p 8080:8080 \
  -p 25:25 \
  -v tempmail-data:/data \
  --env-file .env \
  ghcr.io/xiaozhou26/tempmail:latest
```

| 项 | 说明 |
|----|------|
| HTTP API | `:8080`（`LISTEN_ADDR`） |
| SMTP 收信 | `:25`（`SMTP_ADDR`） |
| 数据库 | `/data/tempmail.db`（请挂 volume） |
| 健康检查 | `GET /healthz` |

### C. systemd

```ini
# /etc/systemd/system/tempmail.service
[Unit]
Description=tempmail temporary mailbox API + SMTP
After=network.target

[Service]
WorkingDirectory=/opt/tempmail
EnvironmentFile=/opt/tempmail/.env
ExecStart=/opt/tempmail/tempmail
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
```

> ⚠️ SMTP 端口 25 需要 root 权限（或通过 `setcap` / `sysctl` 配置）。

### D. 发版（维护者）

推送 `v*` tag 会触发 GitHub Actions：多平台二进制 + GHCR 多架构镜像 + Release 附件。

```bash
git tag v1.0.1
git push origin v1.0.1
```

---

## DNS 配置

要让 `@yourdomain` 的邮件到达本服务，需要：

1. **MX 记录**：指向运行本服务的服务器主机名
2. **A 记录**：该主机名解析到服务器 IP
3. **开放 25 端口**：确保防火墙和云服务商安全组放行

可选但推荐：

```dns
; SPF — 声明本服务器可以发送邮件
yourdomain.  TXT  "v=spf1 mx a ~all"

; DKIM — 防止邮件被标记为垃圾（需要额外配置 DKIM 签名）
; DMARC — 邮件认证策略
_dmarc.yourdomain.  TXT  "v=DMARC1; p=none; rua=mailto:dmarc@yourdomain"
```

> 💡 不再需要 Cloudflare Email Routing。如果域名当前在 Cloudflare，可以直接添加 MX 记录指向本服务器。

---

## 收信配置

### 模式选择

| 模式 | 说明 | 适用场景 |
|------|------|----------|
| **SMTP（推荐）** | 内置 SMTP 服务器直接收信 | 自建服务器，完全自主 |
| IMAP | 从外部中转邮箱拉取 | 共享主机、无法开 25 端口 |
| Graph | 从 Outlook 中转邮箱拉取 | 个人 Outlook 账号 |
| Webhook | 外部推送 | Cloudflare Worker 等 |

各模式可以并存。SMTP 和 IMAP/Graph 可以同时启用。

### SMTP 模式（默认推荐）

```env
SMTP_ENABLED=true
SMTP_ADDR=:25
# SMTP_HOSTNAME=mail.example.com  # 默认等于 MAIL_DOMAIN
```

DNS 配置 MX 记录指向本服务器即可。

### IMAP 模式（兼容旧架构）

```env
IMAP_PROVIDER=firstmail
IMAP_USER=relay@your-firstmail-domain.com
IMAP_PASS=your-password
```

需要配合 Cloudflare Email Routing 的 catch-all 转发。

### Microsoft Graph

```env
GRAPH_ENABLED=true
GRAPH_CLIENT_ID=your-azure-app-client-id
GRAPH_CLIENT_SECRET=your-azure-app-secret
GRAPH_TENANT_ID=common
GRAPH_ACCOUNT=your-relay@outlook.com
GRAPH_REFRESH_TOKEN=your-refresh-token
```

### Webhook

```env
WEBHOOK_SECRET=your-secret
```

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
| GET | `/api/mailboxes/:address` | Key | 详情 + 邮件 |
| DELETE | `/api/mailboxes/:address` | Key | 删邮箱及邮件 |
| GET | `/api/mailboxes/:address/messages` | Key | 邮件列表 |
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
| `MAIL_DOMAIN` | 临时邮箱域名（MX 记录应指向本服务器） |
| `API_KEY` | 管理 API 密钥 |

### SMTP（推荐）

| 变量 | 默认 | 说明 |
|------|------|------|
| `SMTP_ENABLED` | `false` | `true` 启用内置 SMTP 服务器 |
| `SMTP_ADDR` | `:25` | SMTP 监听地址 |
| `SMTP_HOSTNAME` | 等于 `MAIL_DOMAIN` | SMTP EHLO 主机名 |

### 常用可选

| 变量 | 默认 | 说明 |
|------|------|------|
| `LISTEN_ADDR` | `:8080` | HTTP 监听 |
| `DB_PATH` | `./data/tempmail.db` | SQLite 路径（Docker 用 `/data/tempmail.db`） |
| `DEFAULT_TTL_HOURS` | `24` | 新建邮箱默认存活 |
| `CLEANUP_INTERVAL_MIN` | `30` | 过期清理周期（分钟） |
| `WEBHOOK_SECRET` | 空 | 非空则启用 webhook |

### IMAP（兼容旧架构）

| 变量 | 默认 | 说明 |
|------|------|------|
| `IMAP_PROVIDER` | 空 | `firstmail` / `gmail` / `outlook` 预设 |
| `IMAP_HOST` | 空 | 主机 |
| `IMAP_PORT` | `993` | 端口 |
| `IMAP_USER` | 空 | 登录名 |
| `IMAP_PASS` | 空 | 密码 |
| `IMAP_MAILBOX` | `INBOX` | 文件夹 |
| `IMAP_TLS` | `true` | 隐式 TLS |

### Graph（兼容旧架构）

| 变量 | 默认 | 说明 |
|------|------|------|
| `GRAPH_ENABLED` | `false` | `true` 启用 |
| `GRAPH_CLIENT_ID` |  | Azure 应用 |
| `GRAPH_CLIENT_SECRET` |  | 密钥 |
| `GRAPH_TENANT_ID` | `common` | 租户 |
| `GRAPH_ACCOUNT` |  | 中转邮箱地址 |
| `GRAPH_REFRESH_TOKEN` |  | 会轮换写回 `.env` |

---

## 运维建议

1. **API 走 HTTPS**；`API_KEY` 用 `openssl rand -hex 32`
2. **25 端口**可能被云服务商封禁（阿里云、腾讯云等），需申请解封
3. **不要提交** `.env`、`*_tokens.txt`、refresh_token / client_secret
4. 数据在 SQLite 文件；备份即复制 `DB_PATH`
5. 建议配置 SPF/DKIM/DMARC 提高送达率
6. 生产环境建议用 **Caddy/nginx** 反代 HTTPS

---

## 项目结构

```text
.
├── main.go                 # 入口：路由、SMTP 服务器启动、清理
├── config/                 # 环境变量配置
├── models/                 # Mailbox / Message
├── storage/                # SQLite 打开与迁移
├── middleware/             # API Key / Webhook 鉴权
├── handlers/               # HTTP + 入库逻辑
├── smtp/                   # 内置 SMTP 服务器
├── ingest/                 # 按需拉取协调（可选，用于 IMAP/Graph 模式）
├── graph/                  # Microsoft Graph FetchOnce（可选）
├── imap/                   # IMAP FetchOnce（可选）
├── tools/                  # 辅助脚本
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
