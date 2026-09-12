# mail.muskqq.com deployment

## Target layout

- Application: `/opt/tempmail/tempmail`
- Environment: `/opt/tempmail/.env`
- SQLite: `/opt/tempmail/data/tempmail.db`
- HTTP backend: `0.0.0.0:60891`
- Public web: direct high-port HTTP on `60891` (Nginx not used)
- Public SMTP: `25`

The high port applies to the HTTP API. Public mail delivery uses TCP port 25 because MX delivery does not support an arbitrary port.

## DNS

Create these records before requesting the certificate:

| Type | Name | Value | Priority |
|---|---|---|---:|
| A | `mail` | `107.189.29.61` | |
| MX | `@` | `mail.muskqq.com` | 10 |
| TXT | `@` | `v=spf1 mx a ~all` | |

The service receives mail for `@muskqq.com`; keep `MAIL_DOMAIN=muskqq.com`.

## Install and start the application

Build or copy the Linux binary to `/opt/tempmail/tempmail`, then create `/opt/tempmail/.env` with at least:

```env
MAIL_DOMAIN=muskqq.com
API_KEY=REPLACE_WITH_A_LONG_RANDOM_SECRET
SMTP_ENABLED=true
SMTP_ADDR=:25
SMTP_HOSTNAME=mail.muskqq.com
LISTEN_ADDR=0.0.0.0:60891
DB_PATH=/opt/tempmail/data/tempmail.db
DEFAULT_TTL_HOURS=24
CLEANUP_INTERVAL_MIN=30
MESSAGE_TTL_HOURS=24
```

Run:

```bash
sudo mkdir -p /opt/tempmail/data
sudo chown -R root:root /opt/tempmail
sudo chmod 600 /opt/tempmail/.env
sudo install -m 0644 deploy/tempmail.service /etc/systemd/system/tempmail.service
sudo systemctl daemon-reload
sudo systemctl enable --now tempmail
curl -fsS http://127.0.0.1:60891/healthz
```

## Direct high-port access

With Nginx disabled, use the API at:

```text
http://107.189.29.61:60891
http://mail.muskqq.com:60891
```

The DNS A record for `mail.muskqq.com` must point to `107.189.29.61`. The MX record for `muskqq.com` must point to `mail.muskqq.com`. SMTP uses port `25`; HTTP high ports do not affect mail delivery.

## Nginx / HTTPS (not used in the current deployment)

Install Nginx and Certbot, then use the bootstrap config first:

```bash
sudo mkdir -p /var/www/certbot
sudo install -m 0644 deploy/nginx/temp.muskqq.com.bootstrap.conf \
  /etc/nginx/sites-available/mail.muskqq.com
sudo ln -sfn /etc/nginx/sites-available/mail.muskqq.com \
  /etc/nginx/sites-enabled/mail.muskqq.com
sudo nginx -t
sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/certbot \
  -d mail.muskqq.com
```

After the certificate is issued, replace the Nginx config with the HTTPS config:

```bash
sudo install -m 0644 deploy/nginx/temp.muskqq.com.conf \
  /etc/nginx/sites-available/mail.muskqq.com
sudo nginx -t
sudo systemctl reload nginx
curl -fsS https://mail.muskqq.com/healthz
```

Open these firewall ports:

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 25/tcp
```

Do not expose the old `8091` or invalid `80891` ports; the active HTTP port is `60891`.
