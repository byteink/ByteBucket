# ByteBucket

## Description
ByteBucket is a self-hosted, fully S3-compatible object storage system built in Go using the Gin framework. It supports standard S3 operations (uploading, downloading, deleting, and listing objects), as well as bucket management. Metadata is stored in BoltDB, and user credentials (Access Key ID and Secret Access Key) are encrypted. ByteBucket is dockerized with separate configurations for production and development.

---

## Table of Contents
1. [Features](#features)
2. [Prerequisites](#prerequisites)
3. [Installation](#installation)
4. [Running ByteBucket](#running-bytebucket)
    - [Production Mode](#production-mode)
    - [Development Mode](#development-mode)
5. [API Endpoints](#api-endpoints)
    - [Health Check](#health-check)
    - [Buckets](#buckets)
    - [Objects](#objects)
    - [Presigned URLs](#presigned-urls)
6. [Using S3 SDKs](#using-s3-sdks)
    - [AWS SDK for JavaScript (v3)](#aws-sdk-for-javascript-v3)
    - [boto3 (Python)](#boto3-python)
7. [Troubleshooting](#troubleshooting)
8. [Contributing](#contributing)
9. [License](#license)

---

## Features
- **S3 Compatibility:** Supports standard S3 operations (PUT, GET, DELETE, HEAD, LIST).
- **Authentication:** Access Key ID and Secret Access Key using HMAC-SHA256 (AWS Signature v4 compatible).
- **Presigned URLs:** Generate secure, time-limited URLs for object access.
- **Persistent Metadata:** Stores bucket, object, and user metadata in BoltDB.
- **Dockerized:** Separate Dockerfiles and Compose files for production and development.
- **Live Reloading:** Development mode uses Air for automatic reload on changes.

## Prerequisites
- Go 1.24 (or later)
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
1. Build and run with Docker Compose:
   ```bash
   docker-compose -f docker/docker-compose.yml up -d
   ```
2. ByteBucket will be available on port `9000`.

### Development Mode
1. Use the development Dockerfile (`Dockerfile.dev`) and compose file (`docker-compose.dev.yml`) to enable live reloading.
2. From project root:
   ```bash
   docker-compose -f docker/docker-compose.dev.yml up
   ```
3. Access via [http://localhost:9001](http://localhost:9001).
4. Ensure `.air.toml` in project root:
   ```toml
   root = "."
   tmp_dir = "tmp"
   [build]
   cmd = "go build -o tmp/main ./cmd/ByteBucket"
   include_ext = ["go"]
   exclude_dir = ["tmp", "vendor"]
   [log]
   time = true
   ```
5. Add `tmp` to `.dockerignore`.

---

## API Endpoints

### Health Check
- `GET /health`
  ```json
  { "status": "ok" }
  ```

### Buckets
- **Create Bucket:** `POST /buckets/`
  ```json
  { "bucketName": "your_bucket_name" }
  ```
- **List Buckets:** `GET /buckets/`
  ```json
  ["bucket1", "bucket2"]
  ```
- **Delete Bucket:** `DELETE /buckets/{bucketName}`
  ```json
  { "message": "Bucket your_bucket_name deleted" }
  ```

### Objects
- **Upload:** `POST /buckets/{bucketName}/objects/` (multipart/form-data, field `file`)
- **List Objects:** `GET /buckets/{bucketName}/objects`
  ```json
  ["object1", "object2"]
  ```
- **Download (Authenticated):** `GET /buckets/{bucketName}/objects/*objectKey`
- **Delete:** `DELETE /buckets/{bucketName}/objects/*objectKey`
- **Public Access:** Objects with `public-read` ACL accessible at:
  ```
  GET /{bucket}/{objectKey}
  ```
  Example: `http://localhost:9000/mypublicbucket/image.png`

### Presigned URLs *(Dummy Implementation)*
- **Upload URL:** `GET /presign/upload`
- **Download URL:** `GET /presign/download`

---

## Using S3 SDKs
ByteBucket supports standard AWS SDKs by configuring custom endpoints.

### AWS SDK for JavaScript (v3)
```typescript
import { S3Client, GetObjectCommand } from '@aws-sdk/client-s3';
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

async function downloadObject(bucket: string, key: string) {
  const command = new GetObjectCommand({ Bucket: bucket, Key: key });
  const url = await getSignedUrl(s3Client, command, { expiresIn: 900 });
  console.log('Download URL:', url);
}

downloadObject('your_bucket', 'your_object');
```

### boto3 (Python)
```python
import boto3
from botocore.client import Config

s3 = boto3.client('s3',
  endpoint_url='http://localhost:9000',
  aws_access_key_id='your_access_key',
  aws_secret_access_key='your_secret_key',
  config=Config(signature_version='s3v4'),
  region_name='us-east-1')

# List buckets
response = s3.list_buckets()
print(response)

# Generate presigned URL
url = s3.generate_presigned_url('get_object',
  Params={'Bucket': 'your_bucket', 'Key': 'your_object'},
  ExpiresIn=900)

print("Download URL:", url)
```

---

## Troubleshooting
- If Air reports errors, verify `.air.toml` configuration.
- Run `go mod tidy` for dependency issues.
- Verify Docker context in compose files.

---

## Contributing
Contributions are welcome! Fork the repository, make your changes, and open a pull request.

---

## License
Licensed under the [MIT License](LICENSE).