# tempmail API 文档

自建临时域名邮箱后端的 HTTP API。

- 返回格式：**JSON**
- 管理接口鉴权：**API Key**（两种写法等价）
- 邮件列表/详情接口会**先按需拉取**中转邮箱再读库（见 [按需拉信](#按需拉信)）

更完整的部署说明见 [README.md](README.md)。

---

## 目录

- [基础信息](#基础信息)
- [鉴权](#鉴权)
- [接口一览](#接口一览)
- [按需拉信](#按需拉信)
- [接口详情](#接口详情)
- [数据模型](#数据模型)
- [错误约定](#错误约定)
- [客户端集成示例](#客户端集成示例)

---

## 基础信息

| 项 | 值 |
|----|----|
| 默认 Base URL | `http://127.0.0.1:8080` |
| 生产 | HTTPS 反代到 `LISTEN_ADDR` |
| Content-Type | 请求体用 `application/json` |
| 时间字段 | RFC3339（UTC） |

---

## 鉴权

管理接口（`/api/mailboxes*`、`/api/messages*`）必须带 API Key，**二选一**：

```http
Authorization: Bearer <API_KEY>
```

```http
X-API-Key: <API_KEY>
```

缺失或错误时：

```http
HTTP/1.1 401 Unauthorized
```

```json
{ "error": "invalid or missing API key" }
```

`/healthz` **无需**鉴权。  
Webhook 使用独立头 `X-Webhook-Secret`（见下文）。

---

## 接口一览

| 方法 | 路径 | 鉴权 | 按需拉信 | 说明 |
|------|------|------|:--------:|------|
| GET | `/healthz` | 无 | | 健康检查 + 版本 |
| POST | `/api/mailboxes` | API Key | | 创建临时邮箱 |
| GET | `/api/mailboxes` | API Key | | 列出所有邮箱 |
| GET | `/api/mailboxes/:address` | API Key | ✓ | 邮箱详情 + 邮件 |
| DELETE | `/api/mailboxes/:address` | API Key | | 删除邮箱及全部邮件 |
| GET | `/api/mailboxes/:address/messages` | API Key | ✓ | 邮件列表 |
| GET | `/api/messages/:id` | API Key | ✓ | 单封详情（含 `raw`） |
| DELETE | `/api/messages/:id` | API Key | | 删除单封 |
| POST | `/api/webhook/email` | Webhook Secret | | 可选推送入口 |

`:address` 为完整邮箱，如 `alice@mail.example.com`（建议小写）。  
`:id` 为数字主键。

---

## 按需拉信

配置了 Graph 或 IMAP 时，下列接口在返回数据库结果**之前**会触发一次中转箱拉取：

- `GET /api/mailboxes/:address`
- `GET /api/mailboxes/:address/messages`
- `GET /api/messages/:id`

行为摘要：

| 特性 | 说明 |
|------|------|
| 触发时机 | 仅上述 GET；创建邮箱、列表邮箱、删除**不**拉信 |
| 并发 | 多个请求共享一次 in-flight 拉取 |
| 合并窗口 | 约 1 秒内重复请求直接读库 |
| 失败 | 拉取失败会打日志，接口仍尽量返回已有库数据（可能为空/旧） |
| 延迟 | Cloudflare 转发 + 本次 IMAP/Graph RTT；列表接口可能要数秒 |

等验证码时建议客户端 **1～3 秒** 轮询 `.../messages`。

---

## 接口详情

### 健康检查

```http
GET /healthz
```

**响应** `200`

```json
{
  "ok": true,
  "version": "v1.0.0"
}
```

`version` 由构建时 `-ldflags -X main.version=...` 注入；本地 `go run` 一般为 `"dev"`。

---

### 创建临时邮箱

```http
POST /api/mailboxes
Content-Type: application/json
```

**请求体**（均可省略）

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 本地部分（`@` 前）。省略则随机约 10 位 |
| `ttl_hours` | int | 存活小时数。省略用 `DEFAULT_TTL_HOURS` |

**示例**

```bash
curl -X POST "$BASE/api/mailboxes" \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"alice","ttl_hours":12}'
```

**响应** `201`

```json
{
  "id": 1,
  "address": "alice@mail.example.com",
  "name": "alice",
  "created_at": "2026-07-11T08:00:00Z",
  "expires_at": "2026-07-11T20:00:00Z"
}
```

**说明**

- 邮箱是**虚拟**的：Cloudflare catch-all 保证任意地址可收信。`POST` 只是登记便于查询。
- 即使未创建，中转箱来信时也会**自动建箱**（长过期时间）。
- `name` 只保留 `a-z0-9._-`；自定义名冲突 → `409`。

| 状态码 | 含义 |
|--------|------|
| `400` | `name` 非法（过滤后为空） |
| `401` | 鉴权失败 |
| `409` | 地址已存在 |
| `500` | 服务器错误 |

---

### 列出所有邮箱

```http
GET /api/mailboxes
```

按 `created_at` 倒序。列表**不含**邮件正文。

**响应** `200`

```json
[
  {
    "id": 1,
    "address": "alice@mail.example.com",
    "name": "alice",
    "created_at": "2026-07-11T08:00:00Z",
    "expires_at": "2026-07-11T20:00:00Z"
  }
]
```

---

### 邮箱详情 + 邮件

```http
GET /api/mailboxes/:address
```

会先**按需拉信**，再返回邮箱及 `messages`（`received_at` 倒序）。

列表项**不含** `raw`（省流量）。

**响应** `200`

```json
{
  "id": 1,
  "address": "alice@mail.example.com",
  "name": "alice",
  "created_at": "2026-07-11T08:00:00Z",
  "expires_at": "2026-07-11T20:00:00Z",
  "messages": [
    {
      "id": 42,
      "mailbox_id": 1,
      "from": "Sender <noreply@example.com>",
      "to": "alice@mail.example.com",
      "subject": "Your code is 123456",
      "text_body": "Your code is 123456",
      "html_body": "<p>Your code is 123456</p>",
      "received_at": "2026-07-11T08:05:00Z"
    }
  ]
}
```

| 状态码 | 含义 |
|--------|------|
| `404` | 邮箱不存在 |

---

### 列出某邮箱的邮件

```http
GET /api/mailboxes/:address/messages
```

会先**按需拉信**。只返回邮件数组，按 `received_at` 倒序。

**示例**

```bash
curl -H "X-API-Key: $API_KEY" \
  "$BASE/api/mailboxes/alice@mail.example.com/messages"
```

**响应** `200`

```json
[
  {
    "id": 42,
    "mailbox_id": 1,
    "from": "Sender <noreply@example.com>",
    "to": "alice@mail.example.com",
    "subject": "Your code is 123456",
    "text_body": "Your code is 123456",
    "html_body": "<p>Your code is 123456</p>",
    "received_at": "2026-07-11T08:05:00Z"
  }
]
```

空邮箱：`[]`。

| 状态码 | 含义 |
|--------|------|
| `404` | 邮箱不存在 |

---

### 单封邮件详情

```http
GET /api/messages/:id
```

会先**按需拉信**。在列表字段基础上增加 **`raw`**（完整 RFC822）。

- **IMAP / Webhook**：`raw` 通常有内容  
- **Graph**：`raw` 常为空，正文在 `text_body` / `html_body`

**响应** `200`

```json
{
  "id": 42,
  "mailbox_id": 1,
  "from": "Sender <noreply@example.com>",
  "to": "alice@mail.example.com",
  "subject": "Your code is 123456",
  "text_body": "Your code is 123456",
  "html_body": "<p>Your code is 123456</p>",
  "raw": "Received: ...\r\nSubject: Your code is 123456\r\n\r\n...",
  "received_at": "2026-07-11T08:05:00Z"
}
```

| 状态码 | 含义 |
|--------|------|
| `404` | 邮件不存在 |

---

### 删除邮箱

```http
DELETE /api/mailboxes/:address
```

级联删除该邮箱下全部邮件。

**响应** `200`

```json
{ "deleted": "alice@mail.example.com" }
```

| 状态码 | 含义 |
|--------|------|
| `404` | 邮箱不存在 |

---

### 删除单封邮件

```http
DELETE /api/messages/:id
```

**响应** `200`

```json
{ "deleted": "42" }
```

| 状态码 | 含义 |
|--------|------|
| `404` | 邮件不存在 |

---

### Webhook 推送（可选）

```http
POST /api/webhook/email
Content-Type: application/json
X-Webhook-Secret: <WEBHOOK_SECRET>
```

仅当配置了 `WEBHOOK_SECRET` 时可用，否则 `503`。

用于 Cloudflare Email Worker 等主动推送 RFC822（会消耗 Worker 次数）。与 IMAP/Graph 共用存储逻辑。

**请求体**

| 字段 | 类型 | 必填 | 说明 |
|------|------|:----:|------|
| `raw` | string | ✓ | 完整 RFC822 |
| `from` | string | | 提示字段（解析以 raw 为准） |
| `to` | string | | 提示字段 |
| `subject` | string | | 提示字段 |

**成功** `200`

```json
{
  "ok": true,
  "message_id": 42,
  "mailbox": "alice@mail.example.com"
}
```

| 状态码 | 含义 |
|--------|------|
| `202` | 非本域邮件，已忽略 |
| `400` | 缺 `raw` 或 body 非法 |
| `401` | Secret 错误 |
| `422` | 存储失败 |
| `503` | 未配置 `WEBHOOK_SECRET` |

---

## 数据模型

### Mailbox

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int | 主键 |
| `address` | string | 完整地址（唯一） |
| `name` | string | 本地部分 |
| `created_at` | string | 创建时间 |
| `expires_at` | string | 过期时间；后台定时清理 |
| `messages` | array | 仅详情接口可能带上 |

### Message

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int | 主键 |
| `mailbox_id` | int | 所属邮箱 |
| `from` | string | 发件人 |
| `to` | string | 收件人（本域） |
| `subject` | string | 主题 |
| `text_body` | string | 纯文本 |
| `html_body` | string | HTML |
| `raw` | string | RFC822；**仅详情接口**；Graph 常为空 |
| `received_at` | string | 收件时间 |

列表接口不会序列化 `raw`（模型层 `json:"-"`，详情用包装结构补上）。

---

## 错误约定

| 状态码 | 场景 |
|--------|------|
| `400` | 参数非法 |
| `401` | API Key / Webhook Secret 错误 |
| `404` | 资源不存在 |
| `409` | 创建邮箱冲突 |
| `422` | Webhook 业务存储失败 |
| `500` | 内部错误 |
| `503` | Webhook 未启用 |

错误体通常为：

```json
{ "error": "human readable message" }
```

---

## 客户端集成示例

### 创建 → 轮询验证码

```bash
BASE=https://api.yourdomain.com
API_KEY=...

# 创建随机地址
RESP=$(curl -s -X POST "$BASE/api/mailboxes" \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"ttl_hours":1}')
ADDR=$(echo "$RESP" | jq -r .address)
echo "use address: $ADDR"

# 轮询最多 2 分钟
for i in $(seq 1 40); do
  MSGS=$(curl -s -H "X-API-Key: $API_KEY" \
    "$BASE/api/mailboxes/$ADDR/messages")
  COUNT=$(echo "$MSGS" | jq 'length')
  if [ "$COUNT" -gt 0 ]; then
    echo "$MSGS" | jq .
    break
  fi
  sleep 3
done
```

### 从正文提取 6 位数字（示例）

```bash
curl -s -H "X-API-Key: $API_KEY" \
  "$BASE/api/mailboxes/$ADDR/messages" \
  | jq -r '.[0].text_body // .[0].html_body' \
  | grep -oE '[0-9]{4,8}' | head -1
```

### 清理

```bash
curl -s -X DELETE -H "X-API-Key: $API_KEY" \
  "$BASE/api/mailboxes/$ADDR"
```
