# AutoDNS

A lightweight, self-hosted Dynamic DNS monitor that automatically updates a
Cloudflare A record whenever your public WAN IP changes.

## Architecture

```
main.go
  ├── embed (web/index.html → served in-memory)
  ├── internal/store   — bbolt persistence (settings + log history)
  ├── internal/ip      — public IP via api.ipify.org
  ├── internal/cloudflare — Cloudflare DNS API client
  ├── internal/monitor — background ticker goroutine
  └── internal/api     — REST handlers (/api/*)
```

## Quick Start (local)

```bash
go mod tidy
go run .
# Open http://localhost:8080
```

## Environment Variables

| Variable             | Default    | Description                                                       |
|----------------------|------------|-------------------------------------------------------------------|
| `LISTEN_ADDR`        | `:8080`    | TCP address the HTTP server listens on                            |
| `DATA_DIR`           | `./data`   | Directory for the bbolt database file                             |
| `AUTODNS_AUTH_TOKEN` | empty      | Optional API/UI auth token. If set, all `/api/*` calls require it |

## Docker (Portainer)

### Build

```bash
docker build -t autodns:latest .
```

### Run

```bash
docker run -d \
  --name autodns \
  --restart unless-stopped \
  -p 8080:8080 \
  -v autodns-data:/data \
  autodns:latest
```

### docker-compose.yml

```yaml
services:
  autodns:
    image: autodns:latest
    build: .
    container_name: autodns
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - autodns-data:/data

volumes:
  autodns-data:
```

## API Endpoints

| Method | Path            | Description                            |
|--------|-----------------|----------------------------------------|
| GET    | `/api/status`   | Current WAN IP and last-checked time   |
| GET    | `/api/settings` | Read stored Cloudflare settings        |
| POST   | `/api/settings` | Save Cloudflare settings               |
| GET    | `/api/logs`     | Last 20 execution log entries          |
| POST   | `/api/check`    | Trigger an immediate DNS check         |
| POST   | `/api/confirm`  | Confirm initial WAN IP baseline        |

## Cloudflare Setup

1. In the Cloudflare dashboard create an **API Token** with:
   - Permissions: `Zone → DNS → Edit`
   - Zone Resources: the specific zone you want to manage
2. Copy your **Zone ID** from the zone overview page (right sidebar)
3. Enter the token and Zone ID in the AutoDNS settings form
4. Configure either:
  - Record ID (preferred, direct update path), or
  - Record name (e.g. `home.example.co.uk`) so AutoDNS can look up the record ID

## Notes

- Timestamps in the history log are displayed in **Europe/London** time
  using the `DD/MM/YYYY HH:mm:ss` format.
- The database is a single `autodns.db` bbolt file in `DATA_DIR`.
- The binary is fully static (`CGO_ENABLED=0`) and runs in a `scratch`
  container with no shell, no package manager, and no root user.
- Security headers are enabled server-side (CSP, frame denial, no-sniff,
  referrer policy, and permissions policy).
- If `AUTODNS_AUTH_TOKEN` is set, the SPA prompts once and stores the token in
  browser local storage, then sends it as `X-AutoDNS-Token` on API requests.
- For internet exposure, run behind TLS (reverse proxy) and do not publish the
  service without setting `AUTODNS_AUTH_TOKEN`.
