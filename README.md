# zin

A lightweight, zero-downtime API gateway written in Go.

zin sits in front of your backend services and routes public traffic to them. You describe your routes once in a JSON config — which backend serves which path, how long a response may be cached, how many requests a client may make, what happens when a backend goes down — and zin enforces it all at the edge. Edit the config while it runs and every change applies instantly, without dropping a single connection.

It borrows its quality-of-service model from [Krakend](https://www.krakend.io/) (per-endpoint caching, rate limiting, circuit breaking) while staying a single static binary with no runtime dependencies beyond a JSON file.

---

## Table of contents

- [Why zin?](#why-zin)
- [Quick start](#quick-start)
- [Configuration basics](#configuration-basics)
  - [File layout: one file or a directory](#file-layout-one-file-or-a-directory)
  - [Backends and routes](#backends-and-routes)
- [Route features](#route-features)
  - [Response caching](#response-caching)
  - [Rate limiting](#rate-limiting)
  - [Timeouts](#timeouts)
  - [Circuit breaker](#circuit-breaker)
  - [Header manipulation](#header-manipulation)
  - [Static responses](#static-responses)
  - [Path parameters and target rewriting](#path-parameters-and-target-rewriting)
- [Defaults: configure once, inherit everywhere](#defaults-configure-once-inherit-everywhere)
- [The request pipeline](#the-request-pipeline)
- [Hot reload](#hot-reload)
- [Full configuration reference](#full-configuration-reference)
- [Docker deployment](#docker-deployment)
- [Architecture and code layout](#architecture-and-code-layout)
- [Logging](#logging)
- [Limitations to know about](#limitations-to-know-about)
- [Development](#development)
- [License](#license)

---

## Why zin?

A typical setup: a React app talking to three or four services — an auth server, a main API, maybe a function runner. You don't want to expose each service's port to the internet, you don't want to write CORS handling into every service, and you definitely don't want one misbehaving client hammering your login endpoint or one dead backend queuing up every request behind it.

An API gateway solves this at a single point. zin's take:

- **One public port.** Clients only ever see the gateway; services stay private.
- **QoS per endpoint.** "Cache the item list for a minute" and "allow 5 login attempts per minute per IP" are one-line config entries, not code in your services.
- **Resilience built in.** Dead backends trip a circuit breaker instead of piling up timeouts; slow upstreams get cut off with a 504.
- **Zero-downtime reconfiguration.** Add a route by saving a file. No restart, no dropped connections.
- **No dependencies.** One Go binary and JSON files. Nothing to install, no sidecars, no control plane.

If you need JWT validation at the edge, response aggregation across services, or distributed rate limiting across gateway replicas, use Krakend or Traefik. If you want the 80% case in a binary you can read in an afternoon, that's zin.

---

## Quick start

### 1. Build

```bash
go build -o zin .
```

Requires Go 1.22+ (the router uses native `ServeMux` path parameters). The binary lands in your working directory.

### 2. Point it at your services

Create a `config/` directory next to the binary with two files.

`config/config.json` — the entry point:

```json
{
  "listen": ":8080",
  "include": ["backends/*.json"]
}
```

`config/backends/api.json` — one of your services:

```json
{
  "name": "api",
  "url": "http://localhost:3002",
  "routes": [
    { "method": "GET", "pattern": "/items", "cache": { "ttl": "60s" } },
    { "method": "POST", "pattern": "/items" },
    { "method": "GET", "pattern": "/healthz", "static": { "status": 200, "body": { "ok": true } } }
  ]
}
```

### 3. Run it

```bash
./zin
```

That's it. `GET http://localhost:8080/items` now proxies to `http://localhost:3002/items` and caches the response for 60 seconds. `GET /healthz` is answered by the gateway itself. Every subsequent file save in `config/` is picked up automatically.

---

## Configuration basics

### File layout: one file or a directory

zin accepts either:

- **A directory** (recommended) — its `config.json` is the top file and may pull in more files with `"include"` globs. The whole directory tree is watched for changes.
- **A single file** — same schema with the `backends` array written inline. Convenient for containers where you mount exactly one file.

```bash
./zin                       # ./config directory if present, else ./config.json
./zin -config path/to/dir   # explicit directory
./zin -config my.json       # explicit file
```

The include mechanism is how you keep large configs tidy: one file per backend, plus the top file that ties them together.

```text
config/
├── config.json          ← entry point: listen address + include globs
└── backends/
    ├── auth.json        ← one Backend object per file
    ├── api.json
    └── functions.json
```

Globs are resolved relative to the config root. If an included backend has the same `name` as an inline one, the included file wins — so a directory config can selectively override parts of a shared base file.

### Backends and routes

A **backend** is one upstream service: a `name` (your label for it), a `url` (where it lives), and the `routes` it serves. A **route** is one public endpoint: an HTTP method, a URL `pattern`, and optionally any combination of the QoS features described below.

Two rules to keep in mind:

1. **One route, one owner.** The same `method + pattern` cannot appear under two backends — the config loader rejects duplicates instead of silently picking one.
2. **Config is validated strictly.** Unknown field names, invalid durations, malformed URLs: all fail the load with a descriptive error naming the file and field. A typo cannot silently disable a feature you thought was active.

---

## Route features

### Response caching

**What it does.** Stores a successful upstream response in memory and replays it for subsequent identical requests until the TTL expires. The upstream service is not contacted at all during that window.

**Why you want it.** Read-heavy endpoints (catalog lists, config, public profiles) often have identical answers for every caller for minutes at a time. Caching at the gateway takes that load off your services entirely — to the backend, a cached endpoint costs zero requests per second no matter the traffic.

```json
{ "method": "GET", "pattern": "/items", "cache": { "ttl": "60s" } }
```

Behavior details:

- Only `GET` requests use or populate the cache. `POST`/`PUT`/`DELETE` always pass through — caching writes would be wrong.
- Only responses with a `2xx` status are stored. Errors are never replayed.
- The cache key is `method + path + query string`. `/items?page=1` and `/items?page=2` are cached separately.
- Cache hits carry an `X-Cache: HIT` response header so you can verify the feature is working with `curl -i`.
- `"ttl": "0s"` explicitly disables caching — useful to override a backend-wide default (see [defaults](#defaults-configure-once-inherit-everywhere)) for one exception.

> **Caution:** the cache key does not consider `Authorization` or `Cookie` headers. Never enable caching on an endpoint whose response differs per logged-in user. See [limitations](#limitations-to-know-about).

### Rate limiting

**What it does.** Enforces a token bucket per route: at most `max` requests per `per` time window for each key, with burst capacity equal to `max`. Requests over the limit get an immediate `429` and never reach your backend.

**Why you want it.** Login and password-reset endpoints need brute-force protection; public APIs need per-client fairness; expensive endpoints need a ceiling. Doing this at the gateway means the limit applies before your service spends a single cycle on the request.

```json
{
  "method": "POST",
  "pattern": "/login",
  "rate_limit": { "max": 5, "per": "1m", "key": "ip" }
}
```

The `key` decides who shares a bucket:

| Key               | Meaning                                                        | Typical use                          |
| ----------------- | -------------------------------------------------------------- | ------------------------------------ |
| `"global"`        | One bucket for the whole route, all clients share it           | Hard ceiling on an expensive route   |
| `"ip"`            | One bucket per client IP                                       | Brute-force protection per attacker  |
| `"header:X-Key"`  | One bucket per value of the named header                       | Per-API-key limits                   |

When the limit is hit, the response is `429 Too Many Requests` with a `Retry-After` header telling the client how long to wait.

How the refill works: a bucket of `max: 10, per: "1m"` allows 10 requests instantly, then drips back one token every 6 seconds. A client that paced itself at 10/minute continuously never sees a 429.

### Timeouts

**What it does.** Puts a deadline on the entire upstream call for a request. If the backend hasn't finished in time, the gateway cancels the upstream request and answers `504 Gateway Timeout`.

**Why you want it.** Without a timeout, one slow backend holds connections, threads, and client patience hostage indefinitely. With one, the blast radius of a slow endpoint is bounded and visible.

```json
{ "method": "GET", "pattern": "/reports", "timeout": "10s" }
```

Pick values per route cost: health checks a second or two, ordinary reads 5–10s, heavy reports longer. Timeouts also interact with the circuit breaker — a timeout counts as a failure, so consistently-timing-out backends eventually trip it.

### Circuit breaker

**What it does.** Per backend, counts consecutive upstream failures (`5xx` responses and network errors). After `max_failures` in a row the breaker *opens*: for the next `open_timeout` duration, every request to that backend is rejected instantly at the gateway with `503` — no connection attempt, no waiting. After the timeout, one request is allowed through as a *probe*: success closes the breaker, failure re-opens it.

**Why you want it.** When a backend is down, every request costs a connection attempt plus client-side timeout, and your users stare at spinners. An open breaker fails fast instead, which both protects the client experience and gives the struggling backend silence to recover in. This is the classic pattern from [Release It!](https://pragprog.com/titles/mnee2/release-it-second-edition/) and every service mesh.

```json
{
  "name": "api",
  "url": "http://localhost:3002",
  "breaker": { "max_failures": 5, "open_timeout": "30s" },
  "routes": [ ... ]
}
```

`breaker` is configured at the **backend** level because the breaker protects the upstream service as a whole, not individual paths. `max_failures` only counts *consecutive* failures — a success resets the counter, so occasional single errors under load don't trip it.

### Header manipulation

**What it does.** Rewrites headers in both directions: what the upstream receives (`request`) and what the client receives (`response`).

**Why you want it.** Tag upstream traffic so your services know it came through the gateway (and can require it); strip cookies or tracking headers before they reach internal services; remove `Server` fingerprints or internal debug headers from responses before they reach the internet.

```json
{
  "method": "GET",
  "pattern": "/items",
  "headers": {
    "request": {
      "set":    { "X-Forwarded-By": "zin", "X-Internal": "true" },
      "add":    { "X-Trace-Source": "edge" },
      "remove": ["Cookie"]
    },
    "response": {
      "set":    { "X-Gateway": "zin" },
      "remove": ["Server", "X-Internal-Debug"]
    }
  }
}
```

Rules apply in the order **remove → set → add**. `set` replaces all existing values of a header; `add` appends a value; `remove` drops the header entirely. Header names are canonicalized (Go's `textproto` rules), so `"x-api-key"` and `"X-Api-Key"` are the same header.

### Static responses

**What it does.** Makes the gateway answer a route itself with a fixed status, headers, and JSON body — no upstream involved at all.

**Why you want it.** Health checks for the gateway's own liveness, uptime-page endpoints, feature flags a client polls, mock endpoints before the real service exists. A static route costs nothing and keeps working when every backend is down.

```json
{
  "method": "GET",
  "pattern": "/healthz",
  "static": { "status": 200, "headers": { "Cache-Control": "no-store" }, "body": { "ok": true, "version": "1.4.0" } }
}
```

`headers` and `body` are optional; a `body` is served as `application/json`. Static routes can still carry a rate limit or timeout like any other route.

### Path parameters and target rewriting

Routes use Go 1.22 `ServeMux` patterns, so path parameters work natively:

| Pattern                    | Example request    | Captured values                     |
| -------------------------- | ------------------ | ----------------------------------- |
| `/user/{id}`               | `/user/42`         | `id = "42"`                         |
| `/files/{path...}`         | `/files/a/b/c.txt` | `path = "a/b/c.txt"`                |
| `/fn/{short_id}/{rest...}` | `/fn/abc/data`     | `short_id = "abc"`, `rest = "data"` |

**Target rewriting** changes the path sent upstream while keeping the public URL clean. Parameters are substituted from the matched request:

```json
{ "method": "GET", "pattern": "/auth/healthz", "target": "/internal/health" }
{ "method": "GET", "pattern": "/user/{id}", "target": "/internal/users/{id}" }
```

Public `GET /user/42` is proxied to `GET /internal/users/42` on the auth backend. If `target` is omitted, the public pattern is forwarded as-is. Omitting `method` (or `"*"`) matches any method.

---

## Defaults: configure once, inherit everywhere

Backend-level `defaults` are inherited by every route in that backend; a route's own setting always wins. This is how you avoid repeating the same rate limit ten times:

```json
{
  "name": "api",
  "url": "http://localhost:3002",
  "defaults": {
    "timeout": "10s",
    "rate_limit": { "max": 100, "per": "1m", "key": "ip" },
    "headers": { "request": { "set": { "X-Forwarded-By": "zin" } } }
  },
  "routes": [
    { "method": "GET", "pattern": "/items" },
    { "method": "GET", "pattern": "/items/{id}", "cache": { "ttl": "60s" } },
    { "method": "POST", "pattern": "/items", "rate_limit": { "max": 10, "per": "1m", "key": "ip" } },
    { "method": "GET", "pattern": "/uncached", "cache": { "ttl": "0s" } }
  ]
}
```

Every route here gets the 10s timeout and the 100/min per-IP limit. `/items` and `/items/{id}` additionally cache. `POST /items` replaces the inherited limit with a stricter one. `/uncached` shows the explicit opt-out: `"ttl": "0s"` cancels caching for that route even if a default enabled it.

Inheritance is per-field where noted: `timeout`, `rate_limit` and `cache` each inherit independently, while `headers` inherits as a whole object (setting `headers` on a route replaces the default entirely).

---

## The request pipeline

Each route's handler is assembled as a chain of middlewares, outermost first:

```text
              client
                │
                ▼
        ┌──────────────┐
        │  rate limit  │  over budget? → 429, done
        └──────┬───────┘
               ▼
        ┌──────────────┐
        │    cache     │  GET + fresh hit? → replay, done
        └──────┬───────┘
               ▼
        ┌──────────────┐
        │   timeout    │  deadline on everything below
        └──────┬───────┘
               ▼
        ┌──────────────┐
        │ req. headers │  set / add / remove on the outgoing request
        └──────┬───────┘
               ▼
        ┌──────────────┐      ┌─────────────────┐
        │ proxy/static │─────▶│ upstream service │
        └──────┬───────┘      └─────────────────┘
               ▼
        ┌──────────────┐
        │ resp. headers│  set / add / remove on the response
        └──────┬───────┘
               ▼
        ┌──────────────┐
        │ cache store  │  2xx GET? save snapshot
        └──────┬───────┘
               ▼
              client
```

Stages the route doesn't configure are skipped entirely — a route without a rate limit runs no rate-limit code. Two short-circuits matter:

- A **rate-limit rejection** (429) means nothing downstream runs.
- A **cache hit** skips the timeout, headers, and upstream call; the stored response replays in microseconds.

The circuit breaker wraps the proxy call itself: the open check happens just before the upstream request, and the outcome (success/failure) is recorded just after, feeding the state machine described above.

CORS wraps the *entire* pipeline once at the server edge (see below), so preflight `OPTIONS` requests never consume rate-limit budget.

---

## Hot reload

The gateway watches the whole config tree. On any change (after a 200ms debounce to coalesce editor save events):

1. The new config is loaded and **validated completely**.
2. A fresh router — with new middlewares, caches, rate limiters, and breakers — is built off to the side.
3. The old router is swapped out atomically, under a lock that in-flight requests never touch.

Requests that arrived on the old config finish on the old config; every new request uses the new one. No connections are dropped, nothing restarts.

**If the new config is invalid, nothing changes.** The old config keeps serving and the error is logged:

```text
2026/08/23 13:29:52 reload failed, keeping previous config: config/backends/api.json: json: unknown field "ratelimit"
```

This is deliberate: a broken `sudo vim` at 3am should degrade your config editing, not your site.

One consequence to internalize: because the router is rebuilt wholesale, **rate-limit buckets, cached responses, and breaker state reset on every reload**. Editing a config file clears the cache and gives everyone a fresh rate-limit budget. For a small deployment this is the right trade (state can never drift from config); for multi-replica deployments with shared limits, see [limitations](#limitations-to-know-about).

---

## Full configuration reference

Every field, its type, and its default. Durations are strings parsed by Go's `time.ParseDuration`: `"500ms"`, `"10s"`, `"5m"`, `"1h"`.

### Top level

| Field     | Type        | Default      | Description                              |
| --------- | ----------- | ------------ | ---------------------------------------- |
| `listen`  | `string`    | `:8080`      | TCP address to bind (`host:port`)        |
| `include` | `[]string`  | —            | Globs of backend files, relative to root |
| `backends`| `[]Backend` | at least one | Inline backends; may combine with include |

### Backend

| Field      | Type            | Description                                        |
| ---------- | --------------- | -------------------------------------------------- |
| `name`     | `string`        | Unique label; include replaces inline on conflict  |
| `url`      | `string`        | Base URL of the upstream service                   |
| `breaker`  | `Breaker`       | Circuit breaker for this upstream                  |
| `defaults` | `RouteDefaults` | Values inherited by every route                    |
| `routes`   | `[]Route`       | Public endpoints this backend serves               |

### Breaker

| Field          | Type       | Default | Description                                          |
| -------------- | ---------- | ------- | ---------------------------------------------------- |
| `max_failures` | `int`      | —       | Consecutive failures before opening (must be > 0)    |
| `open_timeout` | `duration` | —       | How long it stays open before a half-open probe      |

### RouteDefaults

Any subset of the route fields `timeout`, `cache`, `rate_limit`, `headers`. Inheritance rules are described [above](#defaults-configure-once-inherit-everywhere).

### Route

| Field        | Type       | Default   | Description                                                        |
| ------------ | ---------- | --------- | ------------------------------------------------------------------ |
| `method`     | `string`   | `*`       | `GET`, `POST`, … or `*` for any                                    |
| `pattern`    | `string`   | required  | URL pattern; supports `{id}` and `{rest...}` params                |
| `target`     | `string`   | `pattern` | Upstream path; params substituted before proxying                 |
| `timeout`    | `duration` | —         | Per-request upstream deadline; expiry answers 504                 |
| `cache`      | `Cache`    | —         | `{ "ttl": "60s" }`; `"0s"` disables (overrides a default)          |
| `rate_limit` | `RateLimit`| —         | See below                                                          |
| `headers`    | `Headers`  | —         | See below                                                          |
| `static`     | `Static`   | —         | Gateway answers directly; no upstream call                         |

### Cache

| Field | Type       | Description                                        |
| ----- | ---------- | -------------------------------------------------- |
| `ttl` | `duration` | How long a 2xx GET response is replayed. `0` = off |

### RateLimit

| Field | Type       | Default   | Description                                              |
| ----- | ---------- | --------- | -------------------------------------------------------- |
| `max` | `int`      | required  | Requests per window; also the burst size (must be > 0)   |
| `per` | `duration` | required  | Refill window (must be > 0)                              |
| `key` | `string`   | `global`  | `global`, `ip`, or `header:NAME`                         |

### Headers

| Field     | Type         | Description                            |
| --------- | ------------ | -------------------------------------- |
| `request` | `HeaderRules`| Applied to the request before proxying |
| `response`| `HeaderRules`| Applied to the response before sending |

### HeaderRules

| Field    | Type              | Description                                   |
| -------- | ----------------- | --------------------------------------------- |
| `set`    | `map[string]string`| Replace all values of each named header      |
| `add`    | `map[string]string`| Append one value to each named header        |
| `remove` | `[]string`        | Drop each named header entirely              |

Applied in order: `remove` → `set` → `add`.

### Static

| Field     | Type              | Default | Description                             |
| --------- | ----------------- | ------- | --------------------------------------- |
| `status`  | `int`             | —       | HTTP status to answer with (100–599)    |
| `headers` | `map[string]string` | —     | Extra response headers                  |
| `body`    | JSON value        | —       | Served as `application/json`            |

---

## Docker deployment

The [Dockerfile](Dockerfile) builds a two-stage image: compile in `golang:alpine`, ship the static binary plus `config/` in plain `alpine`.

```bash
docker build -t zin:latest .
docker run -d --name zin -p 8080:8080 -v $(pwd)/config:/app/config zin:latest
```

The volume mount maps your host config directory into the container — the watcher sees host-side edits and reloads live, exactly like a local run.

### Compose with TLS termination

zin speaks plain HTTP; terminate TLS with Caddy or nginx in front. A complete `docker-compose.yml`:

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
      - ./config:/app/config
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

Backends live on the same Docker network (or host ports) and are never published publicly. The `X-Forwarded-For` pass-through matters for `rate_limit` keyed by `ip`: zin trusts that header's first hop when present, so per-client limits survive the extra proxy layer.

---

## Architecture and code layout

```text
┌─────────────┐     ┌─────────────┐     ┌─────────────────┐
│   Client    │────▶│    Caddy    │────▶│      zin        │
│             │     │   (TLS)     │     │     :8080       │
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

The source is organized by responsibility — each package is small, testable, and does one thing:

```text
main.go                  wiring: config discovery, watcher startup, HTTP server
internal/
  config/                schema, duration parsing, include-glob expansion, defaults
                         merge, strict validation, fsnotify watcher (debounced)
  qos/                   quality-of-service primitives, no HTTP knowledge:
                           ratelimit.go  token bucket + per-key registry with idle sweep
                           cache.go      TTL store for response snapshots
                           breaker.go    closed/open/half-open state machine
  httpx/                 HTTP middleware primitives: chain builder, timeout, rate
                         limit, cache, header rules, CORS, response capture
  proxy/                 per-route reverse proxy (path rewrite, response header
                         rules, breaker outcome recording) + static handler
  router/                assembles the ServeMux from config; atomic router swap
  logging/               colored request log to stderr
```

Dependency direction flows downward: `router` composes `proxy` + `httpx`, which use `qos` and `config`; nothing imports back up. The QoS primitives in `qos/` are pure algorithms with injected clocks — that's what makes them unit-testable without HTTP flakiness — and are exercised through full-stack integration tests in `router/`.

The zero-downtime trick is the `AtomicMux` in `internal/router`: the live handler holds a read lock only long enough to grab the current `ServeMux` pointer, so a swap (write lock) never blocks or breaks an in-flight request.

---

## Logging

Every request is logged to stderr, color-coded by method and status:

```text
[14:32:01] GET /user/42 200 -> /internal/users/42
[14:32:02] POST /login 201 -> /login
[14:32:03] GET /healthz 200 -> static
[14:32:04] DELETE /session/abc 503 -> -
```

The right side after `->` is the path sent upstream (`static` for gateway-answered routes, `-` when no upstream call happened). Colors:

| Color  | Meaning                        |
| ------ | ------------------------------ |
| Green  | 2xx status, GET method         |
| Blue   | POST method                    |
| Yellow | PUT method, 3xx status         |
| Cyan   | PATCH method                   |
| Red    | DELETE method, 4xx/5xx status  |

For production, redirect stderr to your log collector; the format is stable and line-oriented.

---

## Limitations to know about

Honest scope boundaries — none of these are bugs, but you should design around them:

- **Cache ignores auth headers.** The key is method + path + query only; there is no `Vary` support. If a response differs per user, don't cache that route.
- **QoS state is per-process.** Rate limits, caches, and breakers live in memory of one gateway instance. Run two replicas and each enforces its own budget (a client gets 2× the limit, effectively). For multi-replica deployments, put a shared limiter (e.g. at Caddy/nginx) in front or accept the multiplier.
- **Reload resets QoS state.** Every config edit rebuilds rate limiters, caches, and breakers. Design operational habits accordingly: don't churn configs on a rate-limited login endpoint.
- **No request body awareness.** Routing and limits consider method/path/headers only; nothing inspects or modifies bodies.
- **Streaming responses are cached whole.** A cached route buffers the upstream response in memory before replaying; route very large payloads (file downloads) away from `cache`.
- **`ip` keying trusts `X-Forwarded-For`.** Correct behind your own reverse proxy; spoofable if the gateway is directly exposed. In that position, prefer `header:` keys.

Planned non-goals for v1 (pull requests welcome): retry with backoff, response aggregation, JWT validation, Prometheus metrics, distributed rate limiting.

---

## Development

```bash
# Run locally against the example config
go run . -config config

# Run the full test suite (unit + integration with stub upstreams)
go test ./...

# Vet and format
go vet ./... && gofmt -l .

# Build a static Linux binary
CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o zin .
```

The test suite covers the QoS primitives with injected clocks (bucket refill, TTL expiry, breaker transitions), config loading (include expansion, defaults merge, strict validation), and full-stack integration tests with stub upstreams asserting cache hit counts, 429 bursts, 504 timeouts, 503 open breakers, header rules in both directions, and path rewriting.

---

## License

MIT
