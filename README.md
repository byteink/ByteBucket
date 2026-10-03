# ByteBucket

Self-hosted, S3-compatible object storage. One small Go binary, a React admin UI, and a filesystem-backed store. Speaks the AWS S3 wire protocol on one port and exposes the same operations through a browser-friendly admin API on another.

```
docker pull ghcr.io/byteink/bytebucket:latest
```

- **S3 API** on port `9000` — AWS Signature V4, XML responses. Works with the AWS SDK, `aws s3`, `rclone`, `s3cmd`, `mc`, Terraform, boto3, anything that speaks S3.
- **Admin API + UI** on port `9001` — header-authenticated JSON surface plus an embedded React dashboard at `/`.
- **Multipart upload**, per-bucket CORS, presigned URLs, real ETags, request IDs, structured JSON logs, Prometheus metrics.

> **Heads up — security.** The admin port (`9001`) must not be exposed to the public internet. Put it behind a private network, VPN, SSH tunnel, or reverse proxy with access control. Details and the deferred-hardening list are in [SECURITY.md](SECURITY.md).

Working on the code? See [DEVELOPMENT.md](DEVELOPMENT.md) for the contributor guide (repo layout, local setup, Vite dev loop, testing, release flow, conventions).

---

## Contents

1. [Quick start](#quick-start)
2. [Configuration](#configuration)
3. [Admin web UI](#admin-web-ui)
4. [S3 API (port 9000)](#s3-api-port-9000)
5. [Admin API (port 9001)](#admin-api-port-9001)
6. [Per-bucket CORS](#per-bucket-cors)
7. [Observability](#observability)
8. [Using it from code](#using-it-from-code)
9. [Storage layout and persistence](#storage-layout-and-persistence)
10. [Limits](#limits)
11. [Troubleshooting](#troubleshooting)
12. [License](#license)

---

## Quick start

### Docker

```bash
docker run -d \
  --name bytebucket \
  -p 9000:9000 \
  -p 9001:9001 \
  -v bytebucket-data:/data \
  -e ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  -e ACCESS_KEY_ID="admin" \
  -e SECRET_ACCESS_KEY="$(openssl rand -base64 32)" \
  ghcr.io/byteink/bytebucket:latest
```

Then open `http://localhost:9001` and log in with the admin access key / secret you just set.

### docker compose

```yaml
services:
  bytebucket:
    image: ghcr.io/byteink/bytebucket:latest
    restart: unless-stopped
    ports:
      - "9000:9000"
      - "9001:9001"
    environment:
      ENCRYPTION_KEY: "32-byte-random-or-base64-encoded-key"
      ACCESS_KEY_ID: "admin"
      SECRET_ACCESS_KEY: "your-strong-secret"
    volumes:
      - bytebucket-data:/data

volumes:
  bytebucket-data:
```

On **first boot** only, the server reads `ACCESS_KEY_ID` / `SECRET_ACCESS_KEY` to create a super-user in BoltDB. After that, those env vars are ignored — rotate credentials through the admin API. `ENCRYPTION_KEY` is required on every boot (it decrypts stored secrets at rest).

---

## Configuration

All configuration is via environment variables.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `ENCRYPTION_KEY` | yes | — | 32 raw bytes or base64-encoded 32-byte key. Encrypts stored user secrets at rest. Lose it, lose every credential. Rotate carefully. |
| `ACCESS_KEY_ID` | first boot only | — | Super-user access key, used once to seed the user database. |
| `SECRET_ACCESS_KEY` | first boot only | — | Super-user secret, same. |
| `GIN_MODE` | no | `release` | Gin mode. Defaults to `release`; set `debug` for local route logging. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | no | `json` | `json` for production / log aggregators, `text` for local dev readability. |
| `PUBLIC_BASE_URL` | no | `http://localhost:9000` | Public origin of the S3 storage surface. Anchors the admin UI's shareable links and server-minted presigned URLs. Defaults to the localhost storage port so presign and public links work out of the box; set it (e.g. `https://bb.example.com`) when ports are remapped or a proxy terminates TLS. |
| `SYNC_WRITES` | no | `true` | Durable object writes: each upload/copy is `fsync`'d (data file + parent directory) before the response returns, so an acknowledged write survives power loss. Set `false` to trade durability for throughput. Toggleable at runtime in the admin Settings UI (a saved override persists and wins over this baseline). |
| `RATE_LIMIT_ENABLED` | no | `false` | Master switch for per-client request rate limiting. Off by default; when disabled the middleware short-circuits after one atomic read, so the per-request cost is negligible. Seeds the baseline that a runtime override (see below) can replace. |
| `RATE_LIMIT_RPS` | no | `0` | Sustained requests per second allowed per client IP (token refill rate). Only meaningful when limiting is enabled. |
| `RATE_LIMIT_BURST` | no | `0` | Token-bucket depth: the largest instantaneous spike one client may make before the sustained `RPS` rate gates it. |
| `RATE_LIMIT_TRUSTED_PROXIES` | no | `0` | Number of reverse-proxy hops in front of the server. Selects which `X-Forwarded-For` entry is the real client. `0` ignores `X-Forwarded-For` and keys on the socket peer. |
| `IP_BAN_ENABLED` | no | `false` | Master switch for the failed-auth IP ban on the S3 port (see below). Off by default; when disabled the middleware short-circuits after one atomic read. Seeds the baseline that a runtime override can replace. |
| `IP_BAN_MAX_FAILURES` | no | `20` | `401`/`403` responses from one client IP that trigger a ban. `1`-`10000`. |
| `IP_BAN_WINDOW_SECONDS` | no | `60` | Window, in seconds, the failures must fall within. `1`-`3600`. |
| `IP_BAN_SECONDS` | no | `900` | Ban length in seconds (15 minutes). Fixed: requests made while banned do not extend it. `1`-`604800` (7 days). |

### Rate limiting

Off by default. Set `RATE_LIMIT_ENABLED=true` to throttle requests per client IP with a token bucket. It runs early in the chain on both ports (after logging/metrics, before auth), so an unauthenticated flood is rejected before it reaches signature verification or the filesystem. Both ports share one per-IP budget, so a client cannot double its allowance by splitting traffic across the S3 and admin surfaces.

- Set `RATE_LIMIT_RPS` (sustained rate) and `RATE_LIMIT_BURST` (spike depth) together. A request that exceeds its bucket gets `503 Slow Down` with a `Retry-After` header — AWS SDKs treat `SlowDown` as retryable and back off automatically.
- **Behind a proxy**, set `RATE_LIMIT_TRUSTED_PROXIES` to the number of hops in front of ByteBucket (e.g. `1` for a single nginx / traefik / ALB). The client IP is resolved by counting that many trusted hops in from the right of `X-Forwarded-For`; the nearest proxy is the connection peer and is not in the header. Leaving it at `0` ignores `X-Forwarded-For` and keys on the socket peer — correct only when ByteBucket is directly exposed. Match it to your actual topology: setting it too low lets a client spoof its limiter key by prepending `X-Forwarded-For` entries.
- The limiter store is bounded (hard entry cap plus idle eviction), so it cannot be turned into a memory-exhaustion vector by an attacker minting source IPs.
- **Runtime override.** The `RATE_LIMIT_*` variables are only the startup baseline. An admin can enable, disable, or retune limiting at runtime from the dashboard's **Settings** page (or `PUT /api/config/ratelimit`) without a restart; changes apply live to both ports. A saved override is persisted and **wins over the environment** until you clear it ("Reset to environment" / `DELETE /api/config/ratelimit`), which reverts to the `RATE_LIMIT_*` baseline.

### Failed-auth IP ban

Off by default. A fail2ban-style guard for the S3 port (`:9000`): a client IP that produces `IP_BAN_MAX_FAILURES` authentication failures (final status `401` or `403`) within `IP_BAN_WINDOW_SECONDS` is refused for `IP_BAN_SECONDS`, then let back in automatically. It is aimed at scanners that guess bucket and key names (`GET /backups/db.sql`, ...) and only ever get `401`.

- **What counts.** Only `401` and `403`. `404` never counts, so a client such as imgproxy fetching deleted objects is never banned; `400`, `429` and `5xx` do not count either.
- **While banned**, every request from that IP gets `403 AccessDenied` (S3 XML, or JSON when the client asks for it) before auth or any handler runs. Banned requests neither count as failures nor extend the ban. One `ip banned` log line (`ip`, `failures`, `ban_until`) is written when a ban starts; blocked requests show up only in the normal access log.
- **Never banned:** loopback, private (RFC 1918, `fc00::/7`), link-local, CGNAT (`100.64.0.0/10`) and unparseable addresses. If the trusted-proxy headers are misconfigured, every request resolves to the reverse proxy's internal address, and banning it would take the whole service down. The flip side: behind a proxy the ban only works once the trusted-proxy headers are set (Settings page), so it sees the real client.
- **Admin port is not covered.** It has its own credential lockout (see [Admin web UI](#admin-web-ui)).
- **Bounded memory.** The failure counters and the ban table are each capped at 65,536 IPs (about 17 MiB worst case in total, measured with full IPv6 keys), with eviction of the oldest entry when full and a background sweep of expired entries.
- **Runtime override.** Like rate limiting, the `IP_BAN_*` variables are only the startup baseline. Change it from the dashboard's **Settings** page or `PUT /api/config/ipban` (`{"enabled":true,"maxFailures":20,"windowSeconds":60,"banSeconds":900}`); `GET` returns `env`, `override` and `effective`, and `DELETE` reverts to the environment. Out-of-range values are rejected with `400`. Saving (or clearing) the setting lifts every active ban; there is no per-IP unban list.

### Ports

| Port | Role | Auth | Expose publicly? |
| --- | --- | --- | --- |
| `9000` | S3 wire protocol | AWS SigV4 | Yes, if that's the point. |
| `9001` | Admin API + web UI + `/metrics` | Web UI: session cookie from `POST /api/login`. Scripts: `X-Admin-AccessKey` + `X-Admin-Secret` headers | **No.** Keep private. |

### Persistence

One volume at `/data`. Layout:

```
/data
  users.db              # BoltDB — users, ACLs, encrypted secrets
  objects/<bucket>/...  # object bytes + .meta / .tags.json / .acl.json / .cors.json sidecars
  uploads/<bucket>/...  # in-flight multipart uploads
```

Back up `/data` as a unit. Objects and metadata are on a filesystem; any snapshot / rsync / restic flow works.

---

## Admin web UI

Port `9001` serves a minimal React dashboard at `/`. It is same-origin with the admin API — no CORS, no AWS SDK in the browser, no third-party calls.

- Log in with the admin access key and secret.
- Manage users and per-user ACLs.
- Create / list / delete buckets.
- Browse, upload, download, delete objects.
- Edit per-bucket CORS as a JSON document.

Signing in posts the access key and secret once to `POST /api/login`. The server checks them in constant time and answers with a random 256-bit session token in a cookie; the browser never stores the secret.

- **Cookie**: `bb_admin_session`, `HttpOnly`, `SameSite=Strict`, `Path=/api`, no `Max-Age` (it ends with the browser). `Secure` is set whenever the request arrived over TLS, directly or through a proxy that sends `X-Forwarded-Proto: https`; plain-http local runs leave it off so `http://localhost:9001` keeps working.
- **Server side**: sessions live in memory, keyed by a SHA-256 hash of the token, capped at 1024 (expired sessions are pruned first, then the least recently used is evicted). They expire after **30 minutes idle** or **8 hours** total, and a restart signs everyone out. Deleting or demoting an admin ends their sessions on the next request.
- **CSRF**: on top of `SameSite=Strict`, every cookie-authenticated request must prove it is same-origin (`Sec-Fetch-Site: same-origin`, or an `Origin` matching the host). Anything else gets `403`.
- **Brute force**: 10 wrong secrets from one client IP within 15 minutes lock that IP out of admin authentication (form and headers alike) with `429` and `Retry-After` until the window ends. Behind a proxy, configure the trusted-proxy headers so the lockout keys on the real client.
- **Logout** (`POST /api/logout`) revokes the session on the server and clears the cookie.
- The UI is served with a strict Content-Security-Policy (no inline script or style, `object-src 'none'`, `frame-ancestors 'none'`), `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

Browsers that ran an older build had the secret in `localStorage` under `bytebucket_session`; the UI deletes that key on every load. The admin port still must not be public. See [SECURITY.md](SECURITY.md).

---

## S3 API (port 9000)

Standard S3 surface. Any S3 client pointed at `http://<host>:9000` with `forcePathStyle: true` and a user's access key / secret works.

### Operations

- **Buckets** — `PUT /:bucket`, `GET /`, `GET /:bucket` (list objects), `DELETE /:bucket`, `HEAD /:bucket`.
- **Objects** — `PUT /:bucket/:key`, `GET /:bucket/:key`, `HEAD /:bucket/:key`, `DELETE /:bucket/:key`. `GET` honours the `Range:` header for partial / resumable downloads, returning `206 Partial Content` with `Content-Range`; `HEAD` and full `GET` advertise `Accept-Ranges: bytes`.
- **Batch delete** — `POST /:bucket?delete` with a `<Delete>` document (up to 1000 keys; `<Quiet>` suppresses the per-key success entries). Each key is validated independently, so a traversal or sidecar-named key is reported as a per-key `<Error>` and never acted on; a key that does not exist deletes successfully (idempotent), matching AWS.
- **Conditional requests** — GET honours `If-Match` / `If-None-Match` / `If-Modified-Since` / `If-Unmodified-Since` (`304 Not Modified` / `412 Precondition Failed`). PUT honours `If-None-Match: *` (create-only) and `If-Match: <etag>` (overwrite-only-if-unchanged) for optimistic concurrency, returning `412` on a mismatch.
- **Copy** — `PUT /:bucket/:key` with an `x-amz-copy-source: /srcbucket/srckey` header. `x-amz-metadata-directive: COPY` (default) carries the source's Content-Type and `x-amz-meta-*`; `REPLACE` takes them from the request. The copy-source is validated like any object key, so a traversal or sidecar-named source is rejected before any read. Returns `<CopyObjectResult>` with the new ETag.
- **Multipart upload** — `POST /:bucket/:key?uploads`, `PUT /:bucket/:key?partNumber=N&uploadId=X`, `POST /:bucket/:key?uploadId=X` (complete), `DELETE /:bucket/:key?uploadId=X` (abort), `GET /:bucket?uploads` (list uploads), `GET /:bucket/:key?uploadId=X` (list parts).
- **Object tagging** — `PUT /:bucket/:key?tagging`, `GET /:bucket/:key?tagging`, `DELETE /:bucket/:key?tagging`. Up to 10 tags per object (key 1-128, value 0-256 UTF-8 chars, no duplicate keys). Tags are stored in a `.tags.json` sidecar independently of object data, so setting or removing them never changes the object's ETag.
- **CORS** — `PUT /:bucket?cors`, `GET /:bucket?cors`, `DELETE /:bucket?cors`.
- **Presigned URLs** — SigV4 `X-Amz-*` query-string style, TTL up to the configured expiry, no server-side state needed.

### Wire format

XML in, XML out. Matches AWS S3 response shapes for `ListAllMyBucketsResult`, `ListBucketResult`, `CORSConfiguration`, `InitiateMultipartUploadResult`, `CompleteMultipartUploadResult`, `DeleteResult`, `CopyObjectResult`, and the standard `<Error>` body. ETags are the hex MD5 of object bytes, quoted. Multipart ETags are `<hex>-<partCount>`, matching S3's composite format.

### Example — put and get via `curl --aws-sigv4`

```bash
export AK=your_access_key
export SK=your_secret_key

# Create a bucket
curl -X PUT http://localhost:9000/my-bucket \
  --aws-sigv4 "aws:amz:us-east-1:s3" --user "$AK:$SK"

# Upload an object
curl -X PUT http://localhost:9000/my-bucket/hello.txt \
  --aws-sigv4 "aws:amz:us-east-1:s3" --user "$AK:$SK" \
  --data-binary 'hello'

# Download it back
curl http://localhost:9000/my-bucket/hello.txt \
  --aws-sigv4 "aws:amz:us-east-1:s3" --user "$AK:$SK"
```

---

## Admin API (port 9001)

All admin API endpoints live under `/api/*` so they cannot collide with the React SPA's client-side routes (`/users`, `/buckets`, `/buckets/:name/cors`, ...) served at the root. `/health` and `/metrics` stay at the root as operational endpoints.

Scripts, CLIs and other non-browser clients authenticate every request with:

```
X-Admin-AccessKey: <your-admin-access-key>
X-Admin-Secret:    <your-admin-secret>
```

Header auth needs no origin checks (the secret is the proof), but wrong secrets count toward the same per-IP lockout as the login form. The web UI uses a session cookie instead (see [Admin web UI](#admin-web-ui)).

### Session

- `POST /api/login` — body `{"accessKey":"...","secret":"..."}`. `200 {"accessKey":"..."}` plus the session cookie; `401` for wrong credentials or a non-admin user; `429` with `Retry-After` while locked out; `403` for a cross-origin browser request.
- `GET /api/session` — `200 {"accessKey":"..."}` for a live session (or valid headers), `401` otherwise.
- `POST /api/logout` — revokes the presented session and clears the cookie. Always `204`, so it is safe to repeat.

### Health

- `GET /health` → `{ "status": "ok" }` — unauthenticated, suitable for readiness probes.

### Users

- `POST /api/users` — create a user. Server generates the access key + secret and returns them **once** in the response. Body takes an `acl` array.
- `GET /api/users` — list users (secrets never returned).
- `PUT /api/users/:accessKeyID` — replace ACL.
- `DELETE /api/users/:accessKeyID` — remove.

**Admin vs regular users.** "Admin" is not a flag — it's an ACL pattern. A user is considered admin (can log in to the dashboard and hit the admin API) if and only if their ACL contains `{"effect":"Allow","buckets":["*"],"actions":["*"]}`. Anything narrower is an S3-only user, scoped to whatever the ACL allows, and cannot access the admin surface. Multiple admins are fine. New users created from the admin UI start with an empty ACL — edit the ACL afterwards to grant exactly the access they need.

Examples:

```json
// Admin — full access, can use the dashboard
{ "acl": [{ "effect": "Allow", "buckets": ["*"], "actions": ["*"] }] }

// Read-only user on one bucket — no dashboard access
{ "acl": [{ "effect": "Allow", "buckets": ["reports"], "actions": ["s3:GetObject", "s3:ListBucket"] }] }

// Write-only uploader — no dashboard, no reads
{ "acl": [{ "effect": "Allow", "buckets": ["uploads"], "actions": ["s3:PutObject"] }] }
```

### S3 operations via the admin surface

Every S3 bucket and object operation is mounted at `/api/s3/*` with a JSON wire format. Same handlers, same storage, just admin auth instead of SigV4. This is what the embedded UI uses; external tooling can use it too.

- `GET /api/s3/` — list buckets.
- `PUT /api/s3/:bucket` — create.
- `GET /api/s3/:bucket` — list objects.
- `DELETE /api/s3/:bucket` — delete.
- `PUT /api/s3/:bucket/:key` — upload (raw body).
- `GET /api/s3/:bucket/:key` — download (raw body).
- `HEAD /api/s3/:bucket/:key` — metadata only.
- `DELETE /api/s3/:bucket/:key` — delete.
- `GET /api/s3/:bucket/:key` honours `Range:` (partial download) exactly as the SigV4 surface does.
- `PUT|GET|DELETE /api/s3/:bucket/:key?tagging` — per-object tags as JSON (`{"tagSet":[{"key":"env","value":"prod"}]}`).
- `PUT|GET|DELETE /api/s3/:bucket?cors` — per-bucket CORS as JSON.

### Example

```bash
export ADMIN_AK=...
export ADMIN_SK=...

# Create a bucket
curl -X PUT http://localhost:9001/api/s3/my-bucket \
  -H "X-Admin-AccessKey: $ADMIN_AK" -H "X-Admin-Secret: $ADMIN_SK"

# Upload an object
curl -X PUT http://localhost:9001/api/s3/my-bucket/hello.txt \
  -H "X-Admin-AccessKey: $ADMIN_AK" -H "X-Admin-Secret: $ADMIN_SK" \
  --data-binary 'hello'

# Create a user with full access
curl -X POST http://localhost:9001/api/users \
  -H "X-Admin-AccessKey: $ADMIN_AK" -H "X-Admin-Secret: $ADMIN_SK" \
  -H "Content-Type: application/json" \
  -d '{"acl":[{"effect":"Allow","buckets":["*"],"actions":["*"]}]}'
```

---

## Per-bucket CORS

CORS lives on the bucket, exactly like AWS S3. There is no global allowlist, no `CORS_ALLOWED_ORIGINS` env var. A bucket with no CORS configuration rejects cross-origin browser requests — that is the S3 contract.

### Endpoints

- `PUT /:bucket?cors` (port 9000, XML body) or `PUT /api/s3/:bucket?cors` (port 9001, JSON body)
- `GET /:bucket?cors` / `GET /api/s3/:bucket?cors`
- `DELETE /:bucket?cors` / `DELETE /api/s3/:bucket?cors`

### JSON shape (admin surface)

```json
{
  "CORSRules": [
    {
      "AllowedMethods": ["GET", "PUT"],
      "AllowedOrigins": ["https://app.example.com"],
      "AllowedHeaders": ["*"],
      "ExposeHeaders":  ["ETag"],
      "MaxAgeSeconds":  600
    }
  ]
}
```

### XML shape (SigV4 surface)

Same grammar as [AWS PutBucketCors](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketCors.html):

```xml
<CORSConfiguration>
  <CORSRule>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedOrigin>https://app.example.com</AllowedOrigin>
    <MaxAgeSeconds>600</MaxAgeSeconds>
  </CORSRule>
</CORSConfiguration>
```

---

## Observability

### Request IDs

Every response carries an `x-amz-request-id` header (UUIDv4, minted per request). Error bodies repeat the same ID as `<RequestId>` in XML or `requestId` in JSON. Use this to correlate a client-visible failure with a server log line.

### Structured logs

One JSON line per request at the end of handling:

```json
{"time":"2026-04-14T07:15:03.882Z","level":"INFO","msg":"http_request","method":"GET","path":"/s3/:bucket","status":200,"duration_ms":3.1,"remote_ip":"10.0.0.4","request_id":"5aba...","auth_method":"sigv4","user_access_key":"AKIA...","bytes_in":0,"bytes_out":482}
```

Stable fields. `path` is always the route template (no object keys, no signatures). Query strings are stripped. Status drives the level: 5xx → `ERROR`, 4xx → `WARN`, else `INFO`. Configure with `LOG_LEVEL` and `LOG_FORMAT`.

### Prometheus metrics

`GET /metrics` on port 9001 serves Prometheus text format. ByteBucket **speaks** the format — it does not bundle a scraper. Point any Prometheus-compatible collector at it (Prometheus, Grafana Agent, VictoriaMetrics, the OpenTelemetry Collector's Prometheus receiver).

Exposed series:

- `http_requests_total{method,path,status}` — counter.
- `http_request_duration_seconds{method,path}` — latency histogram.
- `http_request_size_bytes`, `http_response_size_bytes` — payload histograms.
- `bytebucket_multipart_uploads_in_progress` — gauge.
- `bytebucket_objects_bytes_total{bucket}` — per-bucket byte total (best-effort delta, not reconciled on restart).
- Standard `go_*` and `process_*` collectors.

The endpoint is unauthenticated. It relies on the same network boundary that protects the admin port (see [SECURITY.md](SECURITY.md)).

### Graceful shutdown

On `SIGTERM` or `SIGINT` the server stops accepting new connections and drains in-flight requests for up to 30 seconds before exiting. Kubernetes' default `terminationGracePeriodSeconds` is 30s, which means Shutdown wins the race to SIGKILL in a normal rollout.

---

## Using it from code

### AWS SDK for JavaScript (v3)

```typescript
import { S3Client, PutObjectCommand, GetObjectCommand } from '@aws-sdk/client-s3';
import { getSignedUrl } from '@aws-sdk/s3-request-presigner';

const s3 = new S3Client({
  region: 'us-east-1',
  endpoint: 'http://localhost:9000',
  forcePathStyle: true,
  credentials: { accessKeyId: 'AK', secretAccessKey: 'SK' },
});

await s3.send(new PutObjectCommand({ Bucket: 'b', Key: 'k.txt', Body: 'hi' }));
const url = await getSignedUrl(s3, new GetObjectCommand({ Bucket: 'b', Key: 'k.txt' }), { expiresIn: 900 });
```

Multipart, presigned URLs, and streaming uploads all work as expected.

### boto3 (Python)

```python
import boto3
s3 = boto3.client(
    's3',
    endpoint_url='http://localhost:9000',
    aws_access_key_id='AK',
    aws_secret_access_key='SK',
    region_name='us-east-1',
    config=boto3.session.Config(s3={'addressing_style': 'path'}),
)
s3.upload_file('big.bin', 'my-bucket', 'big.bin')  # uses multipart automatically
```

### `aws` CLI

```bash
aws --endpoint-url http://localhost:9000 s3 cp ./big.bin s3://my-bucket/big.bin
```

### rclone

```
[bytebucket]
type = s3
provider = Other
access_key_id = AK
secret_access_key = SK
endpoint = http://localhost:9000
force_path_style = true
```

### Admin API (any language)

It's plain HTTP + JSON; use `fetch`, `axios`, `requests`, `httpx`, or curl. No SDK, no SigV4.

---

## Storage layout and persistence

Everything lives under `/data`:

```
/data/
  users.db                              # BoltDB
  objects/
    <bucket>/
      <object>                          # raw bytes
      <object>.meta                     # JSON sidecar: ETag, checksums, user metadata
      <object>.tags.json                # JSON sidecar: object tag set (independent of ETag)
      .acl.json                         # per-bucket canned ACL
      .cors.json                        # per-bucket CORS config
  uploads/
    <bucket>/
      <uploadId>/
        manifest.json                   # metadata + state
        <partNumber>                    # raw part bytes
```

- **Backups.** Snapshot the whole `/data` volume. Object bytes + their sidecar must travel together. BoltDB's single file is consistent on snapshot thanks to its write-ahead design.
- **Corruption recovery.** If a `.meta` sidecar is missing, the ETag is recomputed lazily on next read. Stored objects are never mutated after PUT, so bitrot detection is a matter of periodically verifying MD5 against the stored ETag.
- **Deletion** removes the object and its sidecar, then collapses empty parent directories.

---

## Limits

- **Max header size**: 1 MiB.
- **Max request body**: 5 GiB on port 9000 (S3 single-PUT ceiling), 100 MiB on port 9001 (admin surface).
- **Per-connection timeouts**: 10 s on headers, 5 min on read/write, 120 s idle. Very large single-PUT or GET on slow links may hit the 5-min bound; prefer multipart upload for anything above a few hundred MiB.
- **Multipart**: 1 to 10000 parts per upload, no minimum part size enforced (real S3 requires 5 MiB for all but the last part — ByteBucket is lenient).
- **Object tags**: up to 10 per object; key 1-128 and value 0-256 UTF-8 chars; no duplicate keys; tagging document capped at 16 KiB.
- **Rate limiting**: off by default (see [Configuration](#configuration)). When enabled, requests are throttled per client IP and over-limit calls get `503 SlowDown` with `Retry-After`.
- **Failed-auth IP ban**: off by default (see [Failed-auth IP ban](#failed-auth-ip-ban)). When enabled, a public client IP with too many `401`/`403` responses on port 9000 gets `403 AccessDenied` until the ban expires.
- **Presigned URL expiry**: bounded by the request's `X-Amz-Expires` claim; no server-side cap beyond what the client signed.
- **Versioning, object locking, server-side encryption, replication, and lifecycle policies**: not implemented.
- **BoltDB** is a single-writer embedded DB. Fine for up to tens of thousands of users on a single node; don't expect horizontal scale.

---

## Troubleshooting

- **`SignatureDoesNotMatch`** — clock skew between client and server, wrong region (ByteBucket treats all requests as `us-east-1`), or trailing slash / header canonicalisation differences. The error body's `<RequestId>` matches a server log line with the full canonical request trace at `DEBUG`.
- **`NoSuchCORSConfiguration`** on a preflight — set one via the admin UI or the `?cors` endpoint.
- **Admin UI says "Invalid admin credentials"** — the super-user bootstrap only runs when the user DB is empty, so env credentials are ignored after first boot. Check that `ENCRYPTION_KEY` matches what was used on first boot.
- **Admin UI says "Too many failed admin login attempts"** — the client IP hit 10 wrong secrets in 15 minutes. Wait for the `Retry-After` period. If every user shares the lockout, the server sees your proxy's IP: configure the trusted-proxy headers.
- **Admin UI keeps returning to the login page** — the session expired (30 minutes idle, 8 hours total) or the server restarted. Over plain http on a non-localhost address, also check that nothing sets `X-Forwarded-Proto: https`, which marks the cookie `Secure` so the browser drops it.
- **Lost admin credentials or `ENCRYPTION_KEY`** — delete `/data/users.db` and restart with fresh env vars. Objects survive; users and ACLs are gone.
- **Empty `<Owner>` or `dummy-*` in responses** — you're on an older build. Upgrade to `ghcr.io/byteink/bytebucket:latest`.
- **Connection hangs on large uploads** — use multipart. Per-connection write timeout is 5 minutes.
- **Metrics endpoint returns 404** — you hit port 9000. `/metrics` is on 9001.

---

## License

Licensed under the [Server Side Public License](https://www.mongodb.com/licensing/server-side-public-license). Free for open-source and commercial use; offering ByteBucket itself as a managed, paid service requires open-sourcing the complete service stack.
