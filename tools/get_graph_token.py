"""
获取可直接用于 Microsoft Graph 读邮件的 refresh_token。

这是脱敏模板：所有凭据均为占位符，请替换为你自己的 Azure 应用配置。

已验证可用的配置：
  client_id     : 你在 Azure 注册的应用 ID（需授予 Mail.Read / offline_access 委托权限）
  token endpoint: /common（多租户应用）或 /consumers（个人账号）
  redirect_uri  : https://login.microsoftonline.com/common/oauth2/nativeclient
  scope         : offline_access Mail.Read Mail.ReadWrite User.Read

注意：MSA refresh_token 是单次使用，每次换 token 都会轮换。
本脚本只做一次授权换取，拿到后填入 Go 项目 .env 的 GRAPH_REFRESH_TOKEN。
项目运行时会自动把轮换后的新 token 写回 .env。
"""

import sys
import json
import urllib.parse
import requests

# ==================== 请改成你自己的配置（占位符） ====================
CLIENT_ID = "YOUR-AZURE-APP-CLIENT-ID"
CLIENT_SECRET = "YOUR-AZURE-APP-CLIENT-SECRET"   # 应用类型为 Web 时必填；纯公共客户端可留空
YOUR_EMAIL = "your-account@outlook.com"
REDIRECT_URI = "https://login.microsoftonline.com/common/oauth2/nativeclient"
TENANT = "common"   # 多租户用 common；个人账号专用应用可用 consumers
# ====================================================================

SCOPES = "offline_access Mail.Read Mail.ReadWrite User.Read"
AUTH_URL = f"https://login.microsoftonline.com/{TENANT}/oauth2/v2.0/authorize"
TOKEN_URL = f"https://login.microsoftonline.com/{TENANT}/oauth2/v2.0/token"
TOKEN_FILE = "my_graph_tokens.json"


def build_auth_url():
    params = {
        "client_id": CLIENT_ID,
        "response_type": "code",
        "redirect_uri": REDIRECT_URI,
        "scope": SCOPES,
        "response_mode": "query",
        "login_hint": YOUR_EMAIL,
    }
    return f"{AUTH_URL}?{urllib.parse.urlencode(params)}"


def parse_code(callback_url):
    callback_url = callback_url.strip()
    if callback_url.startswith("http"):
        parsed = urllib.parse.urlparse(callback_url)
        qs = urllib.parse.parse_qs(parsed.query)
        if "error" in qs:
            raise SystemExit(f"授权失败: {qs.get('error')} - {qs.get('error_description')}")
        code = qs.get("code", [None])[0]
        if code:
            return code
    raw = callback_url
    if raw.startswith("code="):
        raw = raw[len("code="):]
    if "&" in raw:
        raw = raw.split("&")[0]
    return urllib.parse.unquote(raw)


def exchange_code(code):
    data = {
        "client_id": CLIENT_ID,
        "scope": SCOPES,
        "code": code,
        "redirect_uri": REDIRECT_URI,
        "grant_type": "authorization_code",
    }
    if CLIENT_SECRET:
        data["client_secret"] = CLIENT_SECRET
    r = requests.post(TOKEN_URL, data=data)
    if r.status_code != 200:
        raise SystemExit(f"换 token 失败 {r.status_code}: {r.text}")
    return r.json()


def read_mail(access_token):
    headers = {"Authorization": f"Bearer {access_token}"}
    r = requests.get(
        "https://graph.microsoft.com/v1.0/me/messages?$top=5&$orderby=receivedDateTime DESC",
        headers=headers,
    )
    if r.status_code != 200:
        print("读取失败:", r.status_code, r.text)
        return
    print("✅ Graph 读邮件成功，最近 5 封：")
    for msg in r.json().get("value", []):
        frm = msg.get("from")
        print(f"  [{msg.get('receivedDateTime')}] {msg.get('subject')} <- {frm['emailAddress']['address'] if frm else '?'}")


def main():
    print("=" * 60)
    print("Microsoft Graph refresh_token 获取工具（脱敏模板）")
    print("=" * 60)
    if CLIENT_ID.startswith("YOUR-"):
        print("请先编辑本脚本，把 CLIENT_ID / CLIENT_SECRET / YOUR_EMAIL 改成你自己的。")
        sys.exit(1)

    print("\n【第1步】在浏览器打开下面链接，登录并授权：")
    print(build_auth_url())
    print("\n【第2步】授权后浏览器跳到 nativeclient 地址（打不开正常），把地址栏完整 URL 粘贴回来：")
    code = parse_code(input().strip())

    print("\n【第3步】换取 token ...")
    tokens = exchange_code(code)
    with open(TOKEN_FILE, "w", encoding="utf-8") as f:
        json.dump(tokens, f, indent=2, ensure_ascii=False)
    print(f"✅ 成功，已保存到 {TOKEN_FILE}")
    print("\n" + "=" * 60)
    print("把下面这个 refresh_token 填入 Go 项目 .env 的 GRAPH_REFRESH_TOKEN：")
    print(tokens.get("refresh_token", ""))
    print("=" * 60)

    print("\n【第4步】验证 Graph 能读邮件 ...")
    read_mail(tokens["access_token"])


if __name__ == "__main__":
    main()
