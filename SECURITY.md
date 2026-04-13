# Security Model

ByteBucket ships with a minimal admin UI embedded in the Go binary. The
current authentication and session model is intentionally simple — it is
suitable for a private / localhost deployment only.

## Current model

- Admin authentication: `X-Admin-AccessKey` and `X-Admin-Secret` headers
  verified on every request. There are no cookies or server-side sessions.
- Credentials are stored in the browser's `localStorage` after login. They
  are sent with every admin API call and used to sign S3 requests
  client-side via the AWS SDK.
- S3 authentication: AWS Signature V4 on port 9000.
- CORS configuration is persisted on disk and managed via the admin UI.

## Not for public internet

The admin port (9001) **must not** be exposed directly to the public
internet. Bind it to `127.0.0.1` or a private network, and front it with
a VPN, SSH tunnel, or an authenticated reverse proxy if remote access is
required.

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
