# Security Model

ByteBucket ships with a minimal admin UI embedded in the Go binary. The
current authentication and session model is intentionally simple — it is
suitable for a private / localhost deployment only.

## Current model

- Admin web UI: the access key and secret are posted once to
  `POST /api/login`, compared in constant time, and exchanged for a random
  256-bit session token in a cookie (`HttpOnly`, `SameSite=Strict`,
  `Path=/api`, no expiry attribute, `Secure` whenever the request arrived over
  TLS directly or via `X-Forwarded-Proto: https`). The browser never stores the
  secret; older builds' `localStorage` copy is deleted on load.
- Sessions are held in memory as SHA-256 hashes of the token, capped at 1024,
  and expire after 30 minutes idle or 8 hours total. `POST /api/logout`
  revokes server-side. Deleting or demoting an admin ends their sessions.
- Every cookie-authenticated request must also prove it is same-origin
  (`Sec-Fetch-Site: same-origin`, or an `Origin` matching the host); otherwise
  `403`. This sits on top of `SameSite=Strict`.
- Scripts and CLIs authenticate with `X-Admin-AccessKey` and
  `X-Admin-Secret` headers on every request. The README documents this as the
  admin API for any language, so it stays.
- Brute force: 10 wrong secrets from one client IP within 15 minutes lock that
  IP out of admin authentication (login and headers) until the window ends.
- The admin UI is served with a strict Content-Security-Policy (no inline
  script or style, `object-src 'none'`, `frame-ancestors 'none'`), plus
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and
  `Referrer-Policy: no-referrer`.
- The admin UI talks to storage operations via the same-origin `/api/s3/*`
  surface on port 9001; there is no AWS SDK in the browser and no
  cross-origin call from the UI.
- S3 authentication: AWS Signature V4 on port 9000.
- Failed-auth IP ban (opt-in, off by default): a public client IP with too
  many `401`/`403` responses on port 9000 within a short window is refused
  with `403 AccessDenied` for a fixed period. Loopback, private, link-local,
  CGNAT and unparseable addresses are never banned, so a misconfigured
  trusted-proxy setup cannot ban the proxy and take the service down. Both
  tables are capped, so a botnet minting source IPs cannot exhaust memory.
- CORS is configured per bucket as an S3 subresource (`PUT/GET/DELETE
  /:bucket?cors`). There is no global, user-editable origin allowlist;
  buckets with no configuration reject cross-origin browser requests.

## Not for public internet

The admin port (9001) **must not** be exposed directly to the public
internet. Bind it to `127.0.0.1` or a private network, and front it with
a VPN, SSH tunnel, or an authenticated reverse proxy if remote access is
required.

## Observability

Every response carries an `x-amz-request-id` header (UUIDv4 per request)
and error bodies echo the same value (`<RequestId>` in XML, `requestId`
in JSON). Operators should ship these IDs through their log pipeline so
a client-visible error can be correlated with server-side context.

The admin port also exposes `GET /metrics` in Prometheus text format.
This endpoint is **unauthenticated** on purpose: standard Prometheus
practice is to scrape over a private network and rely on network
boundaries for access control. The existing "do not expose port 9001 to
the public internet" rule already covers this — no separate guidance is
required beyond keeping 9001 bound to localhost or a private subnet.

## Deferred hardening

The following items are known gaps and are tracked for future work:

- TOTP / WebAuthn second factor for the super-user
- In-process TLS termination for the admin port
- Audit log for administrative actions
- Optional IP allow-lists for admin endpoints
