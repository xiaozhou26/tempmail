# 发邮件 API 接口 + 按消息年龄定时清理

日期：2026-07-23

## 概述

为 tempmail 增加两项能力：

1. **发邮件 API**（`POST /api/send`）：通过外部 SMTP 中继或直接 MX 投递向外发送邮件。
2. **按消息年龄定时清理**：在现有清理 ticker 中增加按 `received_at` 年龄删除旧消息的逻辑，控制数据库增长。

---

## 一、发邮件 API

### API 定义

```
POST /api/send
Authorization: Bearer <API_KEY>   （或 X-API-Key，与现有管理接口一致）
Content-Type: application/json
```

请求体：

```json
{
  "from": "alice@yourdomain.com",
  "to": ["bob@example.com", "carol@example.org"],
  "subject": "Hi",
  "text": "plain text body",
  "html": "<p>html body</p>"
}
```

- `to`：接受字符串或字符串数组（自定义 UnmarshalJSON 兼容两种形式）。
- `text` / `html`：至少一个非空；两者都有则发 `multipart/alternative`。
- `from`：可选，若请求未带则用 `SMTP_SEND_FROM` 配置值。

响应：

- 成功：`200 {"ok": true, "accepted": ["bob@example.com", ...]}`
- 部分失败：`200 {"ok": true, "accepted": [...], "rejected": {"carol@example.org": "error msg"}}`
- 校验失败：`400 {"error": "..."}`
- 投递全部失败：`502 {"error": "...", "rejected": {...}}`

### 校验规则

- `from` 域名必须在 `cfg.Domains` 内，否则 `400`（防开放代理 + 符合 SPF）。
- `to` 为空 → `400`。
- `text` 和 `html` 全空 → `400`。
- `from` 为空且无 `SMTP_SEND_FROM` 配置 → `400`。

### 组件

#### `sender/sender.go`（新包）

```go
// Sender 把一封已组装的邮件投递给收件人。
type Sender interface {
    Send(from string, to []string, msg []byte) error
}
```

两种实现：

- **`relaySender`**：通过外部 SMTP 中继发送。
  - 使用 `net/smtp`，支持 STARTTLS（端口 587）和隐式 TLS（端口 465）。
  - 有用户名/密码时用 `smtp.PlainAuth`。
- **`directSender`**：查收件人域名 MX 记录，按优先级逐个尝试直连 25 端口投递。
  - 用 `net.LookupMX`，无 MX 时回退 A 记录。
  - 每个 MX 尝试超时 30 秒。

工厂函数：

```go
// NewSender 根据配置返回 Sender：配置了 SMTP_SEND_HOST 用中继，否则直连。
func NewSender(cfg SMTPSendConfig) Sender
```

#### `handlers/send.go`（新文件）

```go
type SendHandler struct {
    Sender  sender.Sender
    Domains []string
    DefaultFrom string // SMTP_SEND_FROM
}
```

职责：
1. 绑定并校验 JSON 请求。
2. 校验 `from` 域名。
3. 组装 RFC822 报文（`From`/`To`/`Subject`/`Date`/`Message-ID`/`MIME-Version`，正文用 `mime/multipart`）。
4. 调用 `Sender.Send`。
5. 返回结果。

#### 报文组装

- 头：`From`, `To`（逗号拼接）, `Subject`（`mime.WordEncoder` 编码非 ASCII）, `Date`（RFC1123Z）, `Message-ID`（`<uuid@domain>`）, `MIME-Version: 1.0`。
- 正文：
  - 仅 text → `Content-Type: text/plain; charset=utf-8`
  - 仅 html → `Content-Type: text/html; charset=utf-8`
  - 两者 → `multipart/alternative`，text 在前 html 在后。

### 配置（`.env`）

```env
# 可选：外部 SMTP 中继。留空则用直接 MX 投递。
SMTP_SEND_HOST=
SMTP_SEND_PORT=587
SMTP_SEND_USER=
SMTP_SEND_PASS=
SMTP_SEND_STARTTLS=true
SMTP_SEND_FROM=
```

加到 `config.Config`：

```go
type SMTPSendConfig struct {
    Host     string
    Port     int
    User     string
    Pass     string
    StartTLS bool
    From     string // 默认发件人
}
```

`Load()` 中用现有 `get`/`getInt`/`getBool` 读取，无额外校验（全可选）。

### 路由注册（`main.go`）

```go
sendH := &handlers.SendHandler{
    Sender:      sender.NewSender(cfg.SMTPSend),
    Domains:     cfg.Domains,
    DefaultFrom: cfg.SMTPSend.From,
}
mgmt.POST("/send", sendH.Send)
```

### 非目标

- 不落库（不记录发送历史）。
- 不做 DKIM 签名。
- 不做速率限制（后续可加）。

---

## 二、按消息年龄定时清理

### 配置

```env
MESSAGE_TTL_HOURS=24   # 消息保留时长（小时）；0 = 关闭
```

加到 `config.Config`：`MessageTTLHours int`，`Load()` 中用 `getInt("MESSAGE_TTL_HOURS", 24)` 读取。

### 逻辑

在 `EmailHandler` 上新增方法：

```go
// CleanupOldMessages 删除 received_at 早于 maxAge 的消息。
func (h *EmailHandler) CleanupOldMessages(maxAge time.Duration) (int64, error) {
    cutoff := time.Now().Add(-maxAge)
    res := h.DB.Where("received_at < ?", cutoff).Delete(&models.Message{})
    if res.Error != nil {
        return 0, fmt.Errorf("cleanup messages: %w", res.Error)
    }
    return res.RowsAffected, nil
}
```

### 接线（`main.go` `runCleanup`）

在现有 ticker 每次触发时，先跑 `CleanupExpired()`，再跑 `CleanupOldMessages()`：

```go
if cfg.MessageTTLHours > 0 {
    if n, err := h.CleanupOldMessages(time.Duration(cfg.MessageTTLHours) * time.Hour); err != nil {
        log.Printf("cleanup messages error: %v", err)
    } else if n > 0 {
        log.Printf("cleanup: removed %d old messages", n)
    }
}
```

`runCleanup` 签名增加 `msgTTLHours int` 参数：`runCleanup(h, everyMin, msgTTLHours int, stop)`。

### 非目标

- 不清理消息删空后残留的邮箱行（auto-created 邮箱仍按 1 年过期走）。

---

## 三、测试

- `sender` 包的报文组装（`buildMessage`）和域名校验用单元测试覆盖。
- `handlers/send.go` 的请求校验逻辑用 httptest + gin 测试。
- `CleanupOldMessages` 用内存 SQLite 测试。
- 实际网络投递（直连/中继）不写进单测。
- 基线：`go test ./...` 和 `go vet ./...` 通过。

---

## 四、文件变更清单

| 文件 | 操作 |
|------|------|
| `sender/sender.go` | 新建：Sender 接口 + relay/direct 实现 + NewSender |
| `sender/message.go` | 新建：RFC822 报文组装 |
| `handlers/send.go` | 新建：SendHandler + Send 端点 |
| `config/config.go` | 修改：增加 SMTPSendConfig、MessageTTLHours |
| `handlers/email.go` | 修改：增加 CleanupOldMessages 方法 |
| `main.go` | 修改：注册 /api/send 路由、接线清理逻辑 |
| `.env.example` | 修改：增加新配置项说明 |
| `sender/sender_test.go` | 新建：报文组装 + 域名校验测试 |
| `handlers/send_test.go` | 新建：请求校验测试 |
