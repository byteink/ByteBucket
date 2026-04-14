# ByteBucket

## Description
ByteBucket is a self-hosted, S3-compatible object storage system written in Go with the Gin framework. It serves two surfaces from a single process:

- **Port 9000** — the S3 wire protocol: AWS Signature V4 on every request, XML response bodies, consumable by any AWS SDK.
- **Port 9001** — an admin surface with the same storage operations mounted under `/s3/*`, plus user management under `/users`. Authentication is header-based (`X-Admin-AccessKey` + `X-Admin-Secret`), responses are JSON. A minimal embedded React admin UI is served at `/`.

Both surfaces share the same storage handlers; the auth middleware publishes the authenticated user on the request context and handlers stay surface-agnostic.

---

## Table of Contents
1. [Features](#features)
2. [Admin Web UI](#admin-web-ui)
3. [Security / Deployment](#security--deployment)
4. [Prerequisites](#prerequisites)
5. [Installation](#installation)
6. [Running ByteBucket](#running-bytebucket)
7. [Admin API Endpoints](#admin-api-endpoints)
8. [Per-Bucket CORS](#per-bucket-cors)
9. [Using Node.js AWS SDK](#using-nodejs-aws-sdk)
10. [Using Admin API (Node.js)](#using-admin-api-nodejs)
11. [Observability](#observability)
12. [Troubleshooting](#troubleshooting)
13. [Contributing](#contributing)
14. [License](#license)

## Features
- **S3 Compatibility:** Standard S3 operations (PUT, GET, DELETE, HEAD, LIST) on port 9000 via SigV4.
- **Dual-surface storage API:** The same operations are reachable under `/s3/*` on the admin port with JSON wire format for same-origin browser clients.
- **Authentication:** HMAC-SHA256 (AWS Signature V4) for S3; header-based for admin.
- **Presigned URLs:** Time-limited URLs for object access.
- **Per-bucket CORS:** S3-subresource semantics (`PUT/GET/DELETE /:bucket?cors`), no global allowlist.
- **Object metadata:** JSON sidecar next to each object.
- **Real ETags:** Hex MD5 of object bytes, persisted, wire-quoted, backfilled lazily for legacy objects.
- **Per-request IDs:** UUIDv4 emitted as `x-amz-request-id` and echoed in error bodies.
- **Embedded admin UI:** Single Go binary serves API and SPA.
- **Dockerized:** Separate configurations for production and development.

## Admin Web UI
Port 9001 serves a small React admin UI at `http://<host>:9001/`, built with Vite and embedded into the Go binary via `go:embed`. The UI is same-origin with the admin API and talks to `/s3/*` and `/users` directly — no AWS SDK in the browser, no cross-origin requests, no CORS to configure for the admin surface.

- Production: `docker build -f docker/Dockerfile .` (the Dockerfile builds the UI in a node stage before `go build`).
- Local: `make ui && go run ./cmd/ByteBucket`, then open `http://localhost:9001`.
- Dev mode: `cd web && npm run dev` (Vite on :5173 proxies admin API calls to :9001).

Login requires two fields: admin access key and admin secret. Credentials are held in browser `localStorage` and sent on every request as `X-Admin-*` headers.

## Security / Deployment
**The admin port (9001) must not be exposed to the public internet.** Bind it to localhost or a private network. The UI uses simple header-based auth with no session cookies, CSRF protection, or rate limiting. Hardening items (sessions, CSRF, rate limiting, TOTP, in-process TLS) are tracked in [SECURITY.md](SECURITY.md).

## Prerequisites
- Go 1.24 or later
- Docker
- Docker Compose

## Installation
### Clone Repository
```bash
git clone <repository_url>
cd ByteBucket
```

### Set Environment Variables
```bash
export ENCRYPTION_KEY="32characterlongsecretkeyhere1234"
export ACCESS_KEY_ID="your_super_access_key"
export SECRET_ACCESS_KEY="your_super_secret_key"
```

### Update Dependencies
```bash
go mod tidy
```

---

## Running ByteBucket
### Production Mode
```bash
docker compose -f docker/compose.yml up -d
```

### Development Mode
```bash
docker compose -f docker/compose.dev.yml up
```

### Running Tests
```bash
go test -v ./tests/
```

If you encounter Docker build issues while running tests, build the image first:
```bash
docker build -f docker/Dockerfile -t bytebucket-test .
# Then in tests/main_test.go replace the FromDockerfile section with:
#   Image: "bytebucket-test",
```

---

## Admin API Endpoints

All admin endpoints require `X-Admin-AccessKey` and `X-Admin-Secret` headers and are served on port 9001.

### Health Check
- `GET /health` → `{ "status": "ok" }` (unauthenticated)

### User Management
- `POST /users` — create user
- `GET /users` — list users
- `PUT /users/:accessKeyID` — update user
- `DELETE /users/:accessKeyID` — delete user

### S3 Operations via Admin API
Every S3 bucket/object operation is mounted under `/s3/*` with JSON wire format. This is what the embedded admin UI uses; external tooling can use it too without SigV4.

- `GET /s3/` — list buckets
- `PUT /s3/:bucket` — create bucket
- `GET /s3/:bucket` — list objects in bucket
- `DELETE /s3/:bucket` — delete bucket
- `PUT /s3/:bucket/:key` — upload object
- `GET /s3/:bucket/:key` — download object
- `HEAD /s3/:bucket/:key` — object metadata
- `DELETE /s3/:bucket/:key` — delete object
- `PUT|GET|DELETE /s3/:bucket?cors` — per-bucket CORS (see below)

Minimal curl — create a bucket and upload an object via the admin surface:

```bash
curl -X PUT http://localhost:9001/s3/my-bucket \
  -H "X-Admin-AccessKey: your_admin_access_key" \
  -H "X-Admin-Secret: your_admin_secret_key"

curl -X PUT http://localhost:9001/s3/my-bucket/hello.txt \
  -H "X-Admin-AccessKey: your_admin_access_key" \
  -H "X-Admin-Secret: your_admin_secret_key" \
  --data-binary 'hello'
```

---

## Per-Bucket CORS

CORS is configured per bucket as an S3 subresource. There is no global allowlist and no environment variable. A bucket with no CORS configuration rejects cross-origin browser requests — that is the S3 contract.

### Wire formats

- SigV4 surface (port 9000) — S3 XML body, same grammar as AWS S3. See the [AWS PutBucketCors reference](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketCors.html).
- Admin surface (port 9001) — JSON body using storage-layer field names (`AllowedMethods`, `AllowedOrigins`, `AllowedHeaders`, `ExposeHeaders`, `MaxAgeSeconds`).

### Endpoints

- `PUT /:bucket?cors` (9000) or `PUT /s3/:bucket?cors` (9001) — set rules
- `GET /:bucket?cors` (9000) or `GET /s3/:bucket?cors` (9001) — read rules
- `DELETE /:bucket?cors` (9000) or `DELETE /s3/:bucket?cors` (9001) — clear rules

### Example (admin JSON)

```bash
curl -X PUT http://localhost:9001/s3/my-bucket?cors \
  -H "X-Admin-AccessKey: your_admin_access_key" \
  -H "X-Admin-Secret: your_admin_secret_key" \
  -H "Content-Type: application/json" \
  -d '{
    "CORSRules": [{
      "AllowedMethods": ["GET", "PUT"],
      "AllowedOrigins": ["https://app.example.com"],
      "AllowedHeaders": ["*"],
      "ExposeHeaders":  ["ETag"],
      "MaxAgeSeconds":  600
    }]
  }'
```

### Example (S3 XML)

```bash
curl -X PUT "http://localhost:9000/my-bucket?cors" \
  --aws-sigv4 "aws:amz:us-east-1:s3" \
  --user "$ACCESS_KEY:$SECRET_KEY" \
  -H "Content-Type: application/xml" \
  --data-binary '<CORSConfiguration>
    <CORSRule>
      <AllowedMethod>GET</AllowedMethod>
      <AllowedOrigin>https://app.example.com</AllowedOrigin>
      <MaxAgeSeconds>600</MaxAgeSeconds>
    </CORSRule>
  </CORSConfiguration>'
```

---

## Using Node.js AWS SDK
Configure ByteBucket with AWS SDK for JavaScript v3 against the SigV4 surface:

```typescript
import { S3Client, PutObjectCommand, GetObjectCommand } from '@aws-sdk/client-s3';
import { getSignedUrl } from '@aws-sdk/s3-request-presigner';

const s3Client = new S3Client({
  region: 'us-east-1',
  endpoint: 'http://localhost:9000',
  forcePathStyle: true,
  credentials: {
    accessKeyId: 'your_access_key',
    secretAccessKey: 'your_secret_key'
  }
});

async function uploadObject(bucket: string, key: string, body: Buffer | string) {
  const command = new PutObjectCommand({ Bucket: bucket, Key: key, Body: body });
  await s3Client.send(command);
}

async function getPresignedUrl(bucket: string, key: string) {
  const command = new GetObjectCommand({ Bucket: bucket, Key: key });
  return await getSignedUrl(s3Client, command, { expiresIn: 900 });
}

uploadObject('my_bucket', 'my_key.txt', 'Hello ByteBucket!');
getPresignedUrl('my_bucket', 'my_key.txt').then(console.log);
```

---

## Using Admin API (Node.js)
Admin operations and same-origin S3 calls via Axios:

```typescript
import axios from 'axios';

const adminAPI = axios.create({
  baseURL: 'http://localhost:9001',
  headers: {
    'X-Admin-AccessKey': 'your_admin_access_key',
    'X-Admin-Secret': 'your_admin_secret_key',
  },
});

// Create a user
async function createUser() {
  const response = await adminAPI.post('/users', {
    acl: [{ effect: 'Allow', buckets: ['bucket1'], actions: ['*'] }]
  });
  console.log(response.data);
}

// List users
async function listUsers() {
  const response = await adminAPI.get('/users');
  console.log(response.data);
}

// Delete a user
async function deleteUser(accessKeyID: string) {
  await adminAPI.delete(`/users/${accessKeyID}`);
}

// Storage operations via the admin surface (no AWS SDK required)
async function createBucket(bucket: string) {
  await adminAPI.put(`/s3/${bucket}`);
}

async function listObjects(bucket: string) {
  const response = await adminAPI.get(`/s3/${bucket}`);
  console.log(response.data);
}

async function putBucketCORS(bucket: string) {
  await adminAPI.put(`/s3/${bucket}?cors`, {
    CORSRules: [{
      AllowedMethods: ['GET', 'PUT'],
      AllowedOrigins: ['https://app.example.com'],
      AllowedHeaders: ['*'],
      ExposeHeaders:  ['ETag'],
      MaxAgeSeconds:  600,
    }],
  });
}
```

---

## Observability

- Every response carries an `x-amz-request-id` header (UUIDv4 minted per request).
- Error bodies repeat the ID in `<RequestId>` (XML) or `requestId` (JSON). Use it to correlate a client-visible error with server-side logs.
- `<Owner>` in `ListAllMyBuckets` is the authenticated access key (no placeholder).
- `ETag` is the hex MD5 of object bytes, wrapped in double quotes, matching S3's wire format on PUT, GET, HEAD and LIST.

---

## Troubleshooting
- Verify `.air.toml` and Docker configurations if development reload issues occur.
- Run `go mod tidy` for dependency-related errors.
- Deleting an object that doesn't exist returns 204 No Content (success), matching S3 behavior.
- DeleteObject removes both the object and its metadata sidecar.
- Nested object paths (e.g., `folder/subfolder/file.txt`) are supported; parent directories are created on upload and collapsed on delete once empty.
- Every response carries `x-amz-request-id`; error bodies repeat the ID in `<RequestId>` (XML) or `requestId` (JSON). Use this to correlate a client-visible error with server logs.

---

## Contributing
Contributions are welcome. Fork the repository, implement changes, and submit a pull request.

---

## License
Licensed under the [Server Side Public License](https://www.mongodb.com/licensing/server-side-public-license), allowing free use for open-source and commercial products but prohibiting offering the software itself as a managed, paid service without open-sourcing the complete service stack.
