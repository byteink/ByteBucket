# ByteBucket

## Description
ByteBucket is a self-hosted, fully S3-compatible object storage system built in Go using the Gin framework. It supports standard S3 operations (uploading, downloading, deleting, and listing objects), as well as bucket management. User credentials (Access Key ID and Secret Access Key) are encrypted, and object metadata is stored alongside each file as JSON metadata files. ByteBucket is dockerized with separate configurations for production and development.

---

## Table of Contents
1. [Features](#features)
2. [Prerequisites](#prerequisites)
3. [Installation](#installation)
4. [Running ByteBucket](#running-bytebucket)
    - [Production Mode](#production-mode)
    - [Development Mode](#development-mode)
5. [Admin API Endpoints](#admin-api-endpoints)
    - [Health Check](#health-check)
    - [User Management](#user-management)
    - [CORS Configuration](#cors-configuration)
6. [Using Node.js AWS SDK](#using-nodejs-aws-sdk)
7. [Using Admin API (Node.js)](#using-admin-api-nodejs)
8. [Troubleshooting](#troubleshooting)
9. [Contributing](#contributing)
10. [License](#license)

---

## Features
- **S3 Compatibility:** Supports standard S3 operations (PUT, GET, DELETE, HEAD, LIST).
- **Authentication:** Secure HMAC-SHA256 (AWS Signature v4 compatible).
- **Presigned URLs:** Generate secure, time-limited URLs for object access.
- **Object Metadata:** Stored alongside objects as JSON metadata files.
- **Dockerized:** Separate Dockerfiles for production and development environments.
- **Live Reloading:** Automatic reload with Air in development mode.
- **Admin API:** Manage users and access controls via an authenticated RESTful API.
- **Dynamic CORS:** Configure Cross-Origin Resource Sharing with glob pattern support.

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
docker-compose -f docker/docker-compose.yml up -d
```

### Development Mode
```bash
docker-compose -f docker/docker-compose.dev.yml up
```

---

## Admin API Endpoints

### Health Check
- `GET /health`
  ```json
  { "status": "ok" }
  ```

### User Management
- **Create User:** `POST /users`
- **List Users:** `GET /users`
- **Update User:** `PUT /users/:accessKeyID`
- **Delete User:** `DELETE /users/:accessKeyID`

### CORS Configuration
- **Get CORS Config:** `GET /cors`
- **Update CORS Config:** `PUT /cors`
  ```json
  {
    "allowed_origins": ["*.example.com", "app.domain.com"],
    "allowed_methods": ["GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS"],
    "allowed_headers": ["Authorization", "Content-Type", "X-Amz-Date", "..."],
    "expose_headers": ["ETag", "Content-Length", "..."],
    "max_age": 600
  }
  ```

#### Example with curl

```bash
# Get current CORS configuration
curl -X GET http://localhost:9001/cors \
  -H "X-Admin-AccessKey: your_admin_access_key" \
  -H "X-Admin-Secret: your_admin_secret_key"

# Update CORS configuration
curl -X PUT http://localhost:9001/cors \
  -H "X-Admin-AccessKey: your_admin_access_key" \
  -H "X-Admin-Secret: your_admin_secret_key" \
  -H "Content-Type: application/json" \
  -d '{
    "allowed_origins": ["*.example.com", "dashboard.myapp.com"],
    "allowed_methods": ["GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS"],
    "allowed_headers": ["Authorization", "Content-Type", "X-Amz-Date", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-SignedHeaders", "X-Amz-Signature"],
    "expose_headers": ["ETag", "Content-Length", "Content-Type", "Last-Modified"],
    "max_age": 600
  }'
```

---

## Using Node.js AWS SDK
Configure and use ByteBucket with AWS SDK for JavaScript v3:

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

// Upload Object
async function uploadObject(bucket: string, key: string, body: Buffer | string) {
  const command = new PutObjectCommand({ Bucket: bucket, Key: key, Body: body });
  await s3Client.send(command);
}

// Generate Presigned URL for download
async function getPresignedUrl(bucket: string, key: string) {
  const command = new GetObjectCommand({ Bucket: bucket, Key: key });
  return await getSignedUrl(s3Client, command, { expiresIn: 900 });
}

uploadObject('my_bucket', 'my_key.txt', 'Hello ByteBucket!');
getPresignedUrl('my_bucket', 'my_key.txt').then(console.log);
```

---

## Using Admin API (Node.js)
Example of managing users using the Admin API with Axios:

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

// Get CORS configuration
async function getCORSConfig() {
  const response = await adminAPI.get('/cors');
  console.log(response.data);
}

// Update CORS configuration
async function updateCORSConfig() {
  await adminAPI.put('/cors', {
    allowed_origins: ["*.example.com", "subdomain.mydomain.com"],
    allowed_methods: ["GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS"],
    allowed_headers: ["Authorization", "Content-Type"],
    expose_headers: ["ETag", "Content-Length"],
    max_age: 600 // 10 minutes
  });
}

createUser();
listUsers();
```

---

## CORS Configuration

ByteBucket provides flexible Cross-Origin Resource Sharing (CORS) configuration to allow secure API access from web applications.

### Environment Configuration

CORS can be quickly configured using the `CORS_ALLOWED_ORIGINS` environment variable:

```bash
# Configure multiple allowed origins with a comma-separated list
CORS_ALLOWED_ORIGINS=app.mysite.com,api.example.org docker-compose up
```

### Glob Pattern Support

ByteBucket supports wildcards and glob patterns for allowed origins:

- `*` - Allow all origins
- `*.example.com` - Allow all subdomains of example.com
- `example.com/*` - Allow all paths on example.com

### Programmatic Configuration

CORS settings are persisted to disk at `/data/cors.json` and can be updated through the Admin API.

Default CORS settings include:
- All standard HTTP methods
- Common AWS-compatible storage headers
- 10-minute preflight caching

---

## Troubleshooting
- Verify `.air.toml` and Docker configurations if development reload issues occur.
- Run `go mod tidy` for dependency-related errors.

---

## Contributing
Contributions are welcome! Fork the repository, implement changes, and submit a pull request.

---

## License
Licensed under the [Server Side Public License](https://www.mongodb.com/licensing/server-side-public-license), allowing free use for open-source and commercial products but prohibiting offering the software itself as a managed, paid service without open-sourcing the complete service stack.