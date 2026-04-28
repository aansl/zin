# Gateway

A lightweight, zero-downtime API gateway written in Go. Routes traffic across multiple backend services with hot-reload configuration, path parameter substitution, and built-in CORS handling.

---

## Features

- **Zero-downtime hot reload** — edit `config.json` and changes are applied instantly without dropping connections
- **Path parameter routing** — native Go 1.22 `ServeMux` patterns (`{id}`, `{rest...}`)
- **Method-aware matching** — route `GET /items` and `POST /items` to different backends
- **Backend path rewriting** — map public URLs to internal service paths
- **Built-in CORS** — handles preflight and origin headers automatically
- **Colored request logging** — structured stderr output with per-method color coding
- **Single binary, no dependencies** — one static Go binary plus a JSON file

---

## Quick Start

### 1. Build

```bash
go mod init gateway 2>/dev/null || true
go get github.com/fsnotify/fsnotify
go build -o gateway .
```

### 2. Configure

Create `config.json`:

```json
{
  "listen": ":8080",
  "backends": [
    {
      "name": "auth",
      "url": "http://localhost:3001",
      "routes": [
        { "method": "POST", "pattern": "/login" },
        { "method": "GET", "pattern": "/me" },
        { "method": "GET", "pattern": "/user/{id}" }
      ]
    },
    {
      "name": "api",
      "url": "http://localhost:3002",
      "routes": [
        { "method": "GET", "pattern": "/items" },
        { "method": "POST", "pattern": "/items" },
        { "method": "GET", "pattern": "/items/{id}" }
      ]
    }
  ]
}
```

### 3. Run

```bash
./gateway
```

The gateway watches `config.json` for changes and reloads automatically.

---

## Configuration

### Top-level

| Field      | Type        | Default  | Description                       |
| ---------- | ----------- | -------- | --------------------------------- |
| `listen`   | `string`    | `:8080`  | TCP address to bind (`host:port`) |
| `backends` | `[]Backend` | required | List of upstream services         |

### Backend

| Field    | Type      | Description                             |
| -------- | --------- | --------------------------------------- |
| `name`   | `string`  | Human-readable label (used in logs)     |
| `url`    | `string`  | Base URL of the upstream service        |
| `routes` | `[]Route` | URL patterns this backend should handle |

### Route

| Field     | Type     | Default   | Description                                                         |
| --------- | -------- | --------- | ------------------------------------------------------------------- |
| `method`  | `string` | `*`       | HTTP method to match (`GET`, `POST`, etc.) or `*` for any           |
| `pattern` | `string` | required  | URL pattern. Supports Go 1.22 path parameters: `{id}`, `{path...}`  |
| `target`  | `string` | `pattern` | Optional rewrite target. Parameters are substituted before proxying |

### Path Parameters

Use curly braces to capture path segments:

| Pattern                    | Example request    | Captured values                     |
| -------------------------- | ------------------ | ----------------------------------- |
| `/user/{id}`               | `/user/42`         | `id = "42"`                         |
| `/files/{path...}`         | `/files/a/b/c.txt` | `path = "a/b/c.txt"`                |
| `/fn/{short_id}/{rest...}` | `/fn/abc/data`     | `short_id = "abc"`, `rest = "data"` |

### Target Rewriting

Set `target` to change the path sent to the backend while keeping the public URL clean:

```json
{
  "method": "GET",
  "pattern": "/auth/healthz",
  "target": "/healthz"
}
```

Public request `GET /auth/healthz` → proxied to `GET /healthz` on the auth backend.

---

## Docker

### Build

```bash
docker build -t gateway:latest .
```

### Run

```bash
docker run -d \
  --name gateway \
  -p 8080:8080 \
  -v $(pwd)/config.json:/app/config.json \
  gateway:latest
```

The volume mount lets you edit `config.json` on the host; the container picks up changes immediately.

### Compose (with Caddy)

```yaml
services:
  caddy:
    image: caddy:2-alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
    networks:
      - edge

  gateway:
    build: .
    restart: unless-stopped
    volumes:
      - ./config.json:/app/config.json
    networks:
      - edge

networks:
  edge:
    driver: bridge
```

**Caddyfile**

```caddy
yourdomain.com {
    reverse_proxy gateway:8080 {
        header_up Host {host}
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }
}
```

---

## Architecture

```
┌─────────────┐     ┌─────────────┐     ┌─────────────────┐
│   Client    │────▶│    Caddy    │────▶│     Gateway     │
│             │     │   (TLS)     │     │   :8080         │
└─────────────┘     └─────────────┘     └────────┬────────┘
                                                 │
                      ┌──────────────────────────┼──────────────────────────┐
                      │                          │                          │
                      ▼                          ▼                          ▼
              ┌─────────────┐           ┌─────────────┐           ┌─────────────┐
              │    Auth     │           │   Service   │           │  Function   │
              │   :3001     │           │   :3002     │           │   :3004     │
              └─────────────┘           └─────────────┘           └─────────────┘
```

The gateway uses an atomic pointer swap on the request router. When `config.json` changes, a new `ServeMux` is built in the background and swapped in with a write lock. In-flight requests complete on the old router; new requests immediately use the new one.

---

## Logging

All requests are logged to stderr with color-coded output:

```
[14:32:01] GET /user/42 200 -> /user/42
[14:32:02] POST /login 201 -> /login
[14:32:03] DELETE /session/abc 204 -> /session/abc
```

| Color  | Meaning                       |
| ------ | ----------------------------- |
| Green  | 2xx status, GET method        |
| Blue   | POST method                   |
| Yellow | PUT method, 3xx status        |
| Cyan   | PATCH method                  |
| Red    | DELETE method, 4xx/5xx status |

---

## Development

```bash
# Run locally
go run .

# Run tests
go test ./...

# Build static binary for Linux
CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o gateway .
```

---

## License

MIT
