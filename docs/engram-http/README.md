[← Back to README](../../README.md)

# Engram HTTP

**Consume Engram from an MCP client with no binary on the host: run one container, point your client's `http` MCP transport at it.**

Personal/single-user deployment (docker, podman, or a dedicated server). This is **not** a multi-tenant hosted MCP server — every request shares one SQLite database, guarded by a single bearer token or an Engram Cloud principal.

---

## Why

MCP is stdio-only by default (`engram mcp`), so every client host needs the `engram` binary installed. Engram HTTP serves the same MCP tools over streamable HTTP (`engram mcp --transport=http`) from a container, so a machine that cannot install binaries can still use Engram — configuring only a URL and a token in its MCP client.

---

## Quickstart

```bash
cp docker/http/env.example .env   # optional — only needed for a local token or cloud sync
docker compose -f docker-compose.http.yml up -d
curl http://127.0.0.1:7438/health
# {"status":"ok"}
```

This builds `docker/http/Dockerfile` (same multi-stage Go build as the [Engram Cloud image](../engram-cloud/README.md), CGO-free, pure-Go SQLite driver), starts `engram mcp --transport=http --listen=0.0.0.0:7438` as PID 1 of the container, and publishes it on `127.0.0.1:7438` (loopback-only by default — see [Security](#security)). Memories persist in the named volume `engram-http-data` mounted at `/data`.

```bash
docker compose -f docker-compose.http.yml down -v   # stop and drop the volume
```

---

## Client config

```json
{ "mcpServers": { "engram-remote": { "type": "http", "url": "${env:ENGRAM_REMOTE_URL}",
  "headers": { "Authorization": "Bearer ${env:ENGRAM_REMOTE_TOKEN}", "X-Engram-Subproject": "${env:ENGRAM_SUBPROJECT}" } } } }
```

- `ENGRAM_REMOTE_URL` — `http://localhost:7438/mcp` for a local container, or your reverse-proxied HTTPS URL for a remote server.
- `ENGRAM_REMOTE_TOKEN` — the value of `ENGRAM_MCP_HTTP_TOKEN` (local-only mode) or a validated Engram Cloud bearer (cloud mode). Omit the `Authorization` header entirely when neither is configured.
- `ENGRAM_SUBPROJECT` — the project every memory tool call resolves to for this session. There is no cwd fallback over HTTP: a missing or invalid project header returns a clear error instead of silently guessing a project.

`X-Engram-Project` is accepted as an alias for `X-Engram-Subproject`; `X-Engram-Subproject` wins when a client sends both.

---

## Environment variables

| Variable | Purpose |
|---|---|
| `ENGRAM_HTTP_BIND_ADDR` | Host address `docker-compose.http.yml` publishes the container on (default `127.0.0.1`, loopback-only). Set to `0.0.0.0` or a specific interface to reach it from outside the host — see [Server deployment](#server-deployment). |
| `ENGRAM_HTTP_PUBLISHED_PORT` | Host port `docker-compose.http.yml` publishes the container on (default `7438`). |
| `ENGRAM_MCP_HTTP_TOKEN` | Local-only mode: static bearer token required on every `/mcp` request (constant-time compare). Unset disables this guard. |
| `ENGRAM_MCP_HTTP_ALLOWED_ORIGINS` | Comma-separated exact-match `Origin` allowlist. Any request with an `Origin` header is rejected unless listed here. |
| `ENGRAM_MCP_HTTP_ALLOWED_HOSTS` | Comma-separated `Host` allowlist beyond loopback. Checked only when the request has no bound secret (no `ENGRAM_MCP_HTTP_TOKEN`, and in cloud mode only for a bearer-less request falling back to the `.env` token). |
| `ENGRAM_DATA_DIR` | Data directory inside the container. Already `/data`, matching the compose volume mount — only change together with the volume target. |
| `ENGRAM_CLOUD_AUTOSYNC` | Set to `1` to enable cloud mode (see [Auth modes](#auth-modes)). |
| `ENGRAM_CLOUD_SERVER` | Engram Cloud base URL. Must be `https://` in cloud mode, e.g. `https://cloud.example.com` or `https://host.docker.internal:18443` (local `tls` profile). |
| `ENGRAM_CLOUD_TOKEN` | Optional. Fallback/owner cloud token used when a request carries no bearer of its own. Omit it entirely for bearer-only cloud mode — the first request's `Authorization: Bearer` pins the owner and starts sync. |

`ENGRAM_MCP_HTTP_TOKEN` and `ENGRAM_CLOUD_AUTOSYNC=1` are mutually exclusive: the `Authorization` header is either a static local token or an Engram Cloud bearer, never both. `engram` refuses to start if both are set.

The container's *internal* listen address is fixed at `0.0.0.0:7438` by `docker/http/Dockerfile`'s `CMD`. `engram` also supports an `ENGRAM_MCP_HTTP_ADDR` env var for this, but a `--listen` flag always wins, so setting it in `.env` has no effect — use `ENGRAM_HTTP_BIND_ADDR`/`ENGRAM_HTTP_PUBLISHED_PORT` above to control what's reachable from outside the container instead.

---

## Auth modes

### Local-only

No cloud configuration. `ENGRAM_MCP_HTTP_TOKEN`, if set, guards `/mcp`; `/health` stays open for liveness probes. Unset, and bound to loopback (the compose default), the endpoint has no auth at all — fine for a single trusted machine, never for a shared or internet-reachable host.

### Cloud mode

Set `ENGRAM_CLOUD_AUTOSYNC=1` and `ENGRAM_CLOUD_SERVER`. `ENGRAM_CLOUD_TOKEN` is **optional**:

- **With `ENGRAM_CLOUD_TOKEN` set**, every request's bearer must resolve to the **same principal** that owns it, and autosync starts immediately at boot.
- **Without `ENGRAM_CLOUD_TOKEN`** (bearer-only mode), autosync does not start at boot; the first request's bearer that Engram Cloud accepts pins the instance owner for the container's lifetime, and sync starts at that point, using that bearer.

Either way, every request's bearer is validated against the cloud server's `GET /auth/whoami` (cached per token). A bearer from a different account, an invalid bearer, or an unreachable/incompatible cloud server (missing `/auth/whoami`, pre-mutation-endpoint builds) all fail closed:

- Invalid or wrong-account bearer → `401`
- Cloud unreachable, or a cloud server too old to support `/auth/whoami` → `503`

A validated request bearer also becomes the token autosync uses for outbound sync calls for the rest of the process lifetime ("last validated token wins"), and its project is enrolled for cloud sync automatically. `ENGRAM_CLOUD_SERVER` is always required in cloud mode — a missing or invalid server URL is a fatal startup error either way.

`ENGRAM_CLOUD_SERVER` must be `https://`: engram never sends a bearer over plaintext HTTP, so HTTP cloud mode refuses to start with an `http://` URL.

To point this container at a `docker-compose.cloud.yml` stack on the same host, start the cloud with its optional Caddy TLS proxy and trust its local CA:

```bash
# cloud stack, with the TLS proxy published where containers can reach it
# (.env.cloud: ENGRAM_CLOUD_TLS_BIND_ADDR=0.0.0.0)
docker compose -p engram-cloud --env-file .env.cloud --profile tls -f docker-compose.cloud.yml up -d

# trust Caddy's local CA inside engram-http
docker cp engram-cloud-caddy:/data/caddy/pki/authorities/local/root.crt docker/http/ca/engram-cloud-local-ca.crt
chmod 644 docker/http/ca/engram-cloud-local-ca.crt

# in .env
ENGRAM_CLOUD_AUTOSYNC=1
ENGRAM_CLOUD_SERVER=https://host.docker.internal:18443

docker compose -f docker-compose.http.yml up -d --force-recreate
```

`docker-compose.http.yml` maps `host.docker.internal` to the host gateway and mounts `docker/http/ca` into `SSL_CERT_DIR`, so no compose edit is needed. For a remote cloud with a publicly trusted certificate, set `ENGRAM_CLOUD_SERVER` to its real HTTPS URL and leave `docker/http/ca` empty.

Cloud is a **separate image and compose file** ([docker-compose.cloud.yml](../../docker-compose.cloud.yml)) — this file never merges the two services. Bring up cloud first, then engram-http pointed at it. See [Engram Cloud](../engram-cloud/README.md).

---

## Server deployment

Everything this image needs to run on a real server is env-driven — nothing to edit in `docker-compose.http.yml` itself.

```bash
cp docker/http/env.example .env
# edit .env: at minimum set ENGRAM_HTTP_BIND_ADDR and ENGRAM_MCP_HTTP_TOKEN
# (or cloud mode), before publishing beyond loopback
docker compose -f docker-compose.http.yml up -d
```

- **Publish beyond loopback**: set `ENGRAM_HTTP_BIND_ADDR` (e.g. `0.0.0.0`, or a specific interface) and optionally `ENGRAM_HTTP_PUBLISHED_PORT` in `.env`. The container's own internal listen address stays fixed at `0.0.0.0:7438`; only the host-side publish is configurable — see [Environment variables](#environment-variables).
- **Auth**: set `ENGRAM_MCP_HTTP_TOKEN` (local-only mode) or configure cloud mode (see [Auth modes](#auth-modes)) before exposing the port off-host.
- **Allowed hosts/origins**: once exposed, set `ENGRAM_MCP_HTTP_ALLOWED_ORIGINS` and/or `ENGRAM_MCP_HTTP_ALLOWED_HOSTS` to your client's actual origin/host — see [Security](#security).
- **TLS**: this image serves plain HTTP only. Put a TLS-terminating reverse proxy (Caddy, nginx, Traefik) in front for any internet-reachable deployment, and point `ENGRAM_REMOTE_URL` in the client config at the proxy's HTTPS URL.
- **Connecting to a remote cloud**: set `ENGRAM_CLOUD_SERVER` to the cloud instance's real URL (`https://cloud.example.com`, or `https://host.docker.internal:18443` for a cloud stack on the same host with the `tls` profile — see [Cloud mode](#cloud-mode)). `ENGRAM_CLOUD_TOKEN` stays optional either way (bearer-only mode).

---

## Security

- **Loopback by default.** `docker-compose.http.yml` publishes `127.0.0.1:7438:7438` — only reachable from the host machine itself.
- **Exposing beyond loopback** (a shared machine, a LAN, a dedicated server): set `ENGRAM_MCP_HTTP_TOKEN` (local-only mode) or configure cloud mode, and set `ENGRAM_MCP_HTTP_ALLOWED_ORIGINS` / `ENGRAM_MCP_HTTP_ALLOWED_HOSTS` to your client's actual origin/host — these are DNS-rebinding guards, not optional hardening.
- **Remote/internet-reachable servers**: put a TLS-terminating reverse proxy in front (Caddy, nginx, Traefik). This image serves plain HTTP only.
- Chunk/mutation cloud sync traffic is compressed, not encrypted; use HTTPS end to end when cloud mode crosses an untrusted network.

---

## Adopt an existing engram data dir

The container's data directory (`/data`) is a bind-mount target, not just the default named volume. You can point it at a data directory you already have — your host `~/.engram`, or a copy of one moved to a server — instead of starting from an empty database. `engram`'s startup (`store.New`) runs its normal migration sequence against whatever is already there, exactly as it does for any existing local install.

```yaml
# in docker-compose.http.yml, replace the named volume:
volumes:
  - ${HOME}/.engram:/data
```

**Never share a LIVE data directory between a host `engram` process and this container.** SQLite's WAL mode relies on `-shm`/`-wal` sidecar files and OS-level locking that is unreliable across the Docker Desktop VM boundary on macOS and Windows, and two concurrent writers can corrupt the database even on Linux. Either:

- Stop the host `engram` process (and any other engram consuming that directory) before starting the container, or
- Point the mount at a **copy** of the directory instead of the live one — safest on a dedicated server, where you copy `~/.engram` over once and never run a host `engram` against it again.

The container runs as uid `10001`. A bind-mounted host directory keeps its host ownership, so either:

```bash
sudo chown -R 10001:10001 ~/.engram
```

or set on the service instead of chowning the host directory:

```yaml
user: "10001:10001"
```

(only works if that uid/gid can already read/write the mounted path).

---

## Inspect your memories

The terminal UI reads the local SQLite store, so run it in the **engram-http** container:

```bash
docker exec -it -e TERM=xterm-256color engram-http engram tui
```

Running `engram tui` inside the `engram-cloud` container shows nothing: the cloud server keeps its data in Postgres, not in a local SQLite store. Browse synced memories in the cloud dashboard instead (`http://localhost:18080/dashboard`, or your cloud URL), signing in with your cloud token. The dashboard only lists projects allowed by `ENGRAM_CLOUD_ALLOWED_PROJECTS` (use `*` for all).

---

## Next steps

- [Agent Setup](../AGENT-SETUP.md#any-other-mcp-agent) — generic `http` MCP transport wiring
- [Engram Cloud](../engram-cloud/README.md) — the separate cloud image/compose stack this can sync to
- [Full Docs](../../DOCS.md#http-api-endpoints) — complete HTTP API and CLI reference
