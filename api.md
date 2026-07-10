# API 文档

tempmail 后端 API。所有管理接口需要 API Key 鉴权，两种方式任选其一：

- `Authorization: Bearer <API_KEY>`
- `X-API-Key: <API_KEY>`

健康检查 `/healthz` 无需鉴权。

## 基础信息

| 项 | 值 |
|----|----|
| Base URL | `http://localhost:8080`（生产用反代到 HTTPS） |
| 鉴权 | API Key（管理接口） |
| 返回格式 | JSON |

## 接口列表

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| GET | `/healthz` | 无 | 健康检查 |
| POST | `/api/mailboxes` | API Key | 创建临时邮箱 |
| GET | `/api/mailboxes` | API Key | 列出所有邮箱 |
| GET | `/api/mailboxes/:address` | API Key | 邮箱详情 + 邮件列表 |
| DELETE | `/api/mailboxes/:address` | API Key | 删除邮箱及其邮件 |
| GET | `/api/mailboxes/:address/messages` | API Key | 列出该邮箱的邮件 |
| GET | `/api/messages/:id` | API Key | 单封邮件详情 |
| DELETE | `/api/messages/:id` | API Key | 删除单封邮件 |
| POST | `/api/webhook/email` | Webhook Secret | 可选推送入口（默认禁用） |

---

## 健康检查

```
GET /healthz
```

无需鉴权。

**响应**

```json
{
  "ok": true
}
```

---

## 创建临时邮箱

```
POST /api/mailboxes
```

**请求体**（可选）

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 自定义本地部分（`@` 前）。省略则随机生成 |
| `ttl_hours` | int | 存活时长（小时）。省略用服务端默认值 |

**请求示例**

```bash
curl -X POST https://api.yourdomain.com/api/mailboxes \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"alice","ttl_hours":12}'
```

**响应** `201 Created`

```json
{
  "id": 1,
  "address": "alice@yourdomain.com",
  "name": "alice",
  "created_at": "2026-07-10T12:00:00Z",
  "expires_at": "2026-07-11T00:00:00Z"
}
```

**说明**

- 邮箱是**虚拟**的——catch-all 保证任何地址都能收信。`POST /api/mailboxes` 只是登记入库以便查询；即便没登记，收到信时后端也会自动建一个。
- 自定义 `name` 只保留 `a-z0-9._-`，其余字符被过滤；重名返回 `409`。
- 不带 `name` 时随机生成 10 位本地部分。

**错误**

| 状态码 | 含义 |
|--------|------|
| `400` | `name` 非法（过滤后为空） |
| `409` | 地址已存在 |
| `500` | 数据库错误 |

---

## 列出所有邮箱

```
GET /api/mailboxes
```

按创建时间倒序。

**响应** `200`

```json
[
  {
    "id": 1,
    "address": "alice@yourdomain.com",
    "name": "alice",
    "created_at": "2026-07-10T12:00:00Z",
    "expires_at": "2026-07-11T00:00:00Z"
  }
]
```

> 列表里不包含邮件；用邮箱详情接口获取邮件。

---

## 邮箱详情 + 邮件列表

```
GET /api/mailboxes/:address
```

返回单个邮箱，并在 `messages` 字段带上其邮件（按收件时间倒序）。

**响应** `200`

```json
{
  "id": 1,
  "address": "alice@yourdomain.com",
  "name": "alice",
  "created_at": "2026-07-10T12:00:00Z",
  "expires_at": "2026-07-11T00:00:00Z",
  "messages": [
    {
      "id": 42,
      "mailbox_id": 1,
      "from": "sender@example.com",
      "to": "alice@yourdomain.com",
      "subject": "Hello",
      "text_body": "...",
      "html_body": "<html>...</html>",
      "received_at": "2026-07-10T12:05:00Z"
    }
  ]
}
```

**错误**

| 状态码 | 含义 |
|--------|------|
| `404` | 邮箱不存在 |

---

## 列出某邮箱的邮件

```
GET /api/mailboxes/:address/messages
```

只返回邮件，按收件时间倒序，不含邮箱信息。

**响应** `200`

```json
[
  {
    "id": 42,
    "mailbox_id": 1,
    "from": "sender@example.com",
    "to": "alice@yourdomain.com",
    "subject": "Hello",
    "text_body": "...",
    "html_body": "<html>...</html>",
    "received_at": "2026-07-10T12:05:00Z"
  }
]
```

**错误**

| 状态码 | 含义 |
|--------|------|
| `404` | 邮箱不存在 |

---

## 单封邮件详情

```
GET /api/messages/:id
```

返回单封邮件。详情接口会额外包含完整 RFC822 原文 `raw`（仅 IMAP/webhook 模式有；Graph 模式 `raw` 为空，正文在 `html_body` / `text_body`）。

**响应** `200`

```json
{
  "id": 42,
  "mailbox_id": 1,
  "from": "sender@example.com",
  "to": "alice@yourdomain.com",
  "subject": "Hello",
  "text_body": "...",
  "html_body": "<html>...</html>",
  "raw": "Received: ...\r\nSubject: Hello\r\n\r\n...",
  "received_at": "2026-07-10T12:05:00Z"
}
```

**错误**

| 状态码 | 含义 |
|--------|------|
| `404` | 邮件不存在 |

---

## 删除邮箱

```
DELETE /api/mailboxes/:address
```

删除邮箱及其所有邮件（级联删除）。

**响应** `200`

```json
{
  "deleted": "alice@yourdomain.com"
}
```

**错误**

| 状态码 | 含义 |
|--------|------|
| `404` | 邮箱不存在 |

---

## 删除单封邮件

```
DELETE /api/messages/:id
```

**响应** `200`

```json
{
  "deleted": "42"
}
```

**错误**

| 状态码 | 含义 |
|--------|------|
| `404` | 邮件不存在 |

---

## Webhook 推送入口（可选，默认禁用）

```
POST /api/webhook/email
```

仅当配置了 `WEBHOOK_SECRET` 时启用，否则返回 `503`。请求头需带 `X-Webhook-Secret`。

用于 Email Worker 主动推送邮件（会消耗 Worker 次数），与 Graph/IMAP 共用同一存储逻辑，互不冲突。

**请求头**

```
X-Webhook-Secret: <WEBHOOK_SECRET>
Content-Type: application/json
```

**请求体**

| 字段 | 类型 | 说明 |
|------|------|------|
| `from` | string | 发件人 |
| `to` | string | 收件人 |
| `subject` | string | 主题 |
| `raw` | string | 完整 RFC822 原文（必填） |

**响应**

| 状态码 | 含义 |
|--------|------|
| `200` | 已存储，返回 `{"ok":true,"message_id":..,"mailbox":..}` |
| `202` | 非本域邮件，已忽略 |
| `400` | 缺少 `raw` 字段或请求体非法 |
| `401` | webhook secret 错误 |
| `503` | webhook 未配置 |
| `422` | 存储失败 |

---

## 数据模型

### Mailbox

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int | 主键 |
| `address` | string | 完整邮箱地址（唯一） |
| `name` | string | 本地部分 |
| `created_at` | datetime | 创建时间 |
| `expires_at` | datetime | 过期时间 |
| `messages` | array | 邮件列表（仅详情接口返回） |

### Message

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int | 主键 |
| `mailbox_id` | int | 所属邮箱 ID |
| `from` | string | 发件人 |
| `to` | string | 收件人（本域地址） |
| `subject` | string | 主题 |
| `text_body` | string | 纯文本正文 |
| `html_body` | string | HTML 正文 |
| `raw` | string | RFC822 原文（仅详情接口；Graph 模式为空） |
| `received_at` | datetime | 收件时间（UTC） |

---

## 鉴权错误

所有需要鉴权的接口，API Key 错误或缺失时返回：

```json
{
  "error": "invalid or missing API key"
}
```

状态码 `401`。
