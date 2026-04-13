// Builds an S3Client configured for the ByteBucket storage endpoint.
// Path-style addressing is required; the server does not support virtual-host style.

import { ListBucketsCommand, S3Client } from '@aws-sdk/client-s3';
import type { Session } from './session';

export function makeS3Client(s: Session): S3Client {
  return new S3Client({
    region: 'us-east-1',
    endpoint: s.storageEndpoint,
    forcePathStyle: true,
    credentials: {
      accessKeyId: s.accessKey,
      secretAccessKey: s.secret,
    },
  });
}

export interface S3ProbeResult {
  ok: boolean;
  message?: string;
}

// probeS3 classifies the three failure modes of the login S3 reachability check.
export async function probeS3(client: S3Client): Promise<S3ProbeResult> {
  try {
    await client.send(new ListBucketsCommand({}));
    return { ok: true };
  } catch (err: unknown) {
    const e = err as { name?: string; $metadata?: { httpStatusCode?: number }; message?: string };
    const status = e?.$metadata?.httpStatusCode;
    const name = e?.name ?? '';
    if (status === 403 || name === 'SignatureDoesNotMatch' || name === 'InvalidAccessKeyId') {
      return { ok: false, message: 'S3 endpoint reachable but credentials rejected' };
    }
    if (status === undefined) {
      return {
        ok: false,
        message:
          'Cannot reach S3 endpoint. Check the URL and CORS settings in the UI after login.',
      };
    }
    return { ok: false, message: e?.message ?? 'Unknown S3 error' };
  }
}
