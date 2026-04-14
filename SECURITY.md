# Security Model

ByteBucket ships with a minimal admin UI embedded in the Go binary. The
current authentication and session model is intentionally simple — it is
suitable for a private / localhost deployment only.

## Current model

- Admin authentication: `X-Admin-AccessKey` and `X-Admin-Secret` headers
  verified on every request. There are no cookies or server-side sessions.
- Credentials are stored in the browser's `localStorage` after login and
  sent as `X-Admin-*` headers on every admin API call. The admin UI talks
  to storage operations via the same-origin `/s3/*` surface on port 9001;
  there is no AWS SDK in the browser and no cross-origin call from the UI.
- S3 authentication: AWS Signature V4 on port 9000.
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

- Server-side sessions with short-lived tokens (replace header-auth on
  admin)
- CSRF protection for the admin API
- Rate limiting / brute-force protection on admin auth
- TOTP / WebAuthn second factor for the super-user
- In-process TLS termination for the admin port
- Audit log for administrative actions
- Optional IP allow-lists for admin endpoints
