// ObjectPreview renders an object inline by Content-Type. Media (image, video,
// audio, PDF) streams from a short-lived presigned URL so the bytes never pass
// through JS memory; the blob path only exists for servers with no
// PUBLIC_BASE_URL, where presigning answers 503. Text always uses a capped
// blob slice so a 50 MB log file cannot lock the tab.
import { useEffect, useState, type ReactNode } from 'react';
import { getObject, presignObject } from '../lib/s3';
import type { Session } from '../lib/session';
import { errorMessage } from '../lib/format';
import { IconButton, Loading, Seg } from './ui';

const TEXT_LIMIT = 256 * 1024;
const PRESIGN_TTL = 3600;

export type PreviewKind = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'none';

const TEXT_TYPES = new Set([
  'application/json',
  'application/xml',
  'application/javascript',
  'application/x-yaml',
  'application/yaml',
]);

// Everything outside this allowlist renders "no preview" so an unknown blob
// is never embedded into the page.
export function previewKind(contentType: string): PreviewKind {
  const ct = contentType.toLowerCase();
  if (ct.startsWith('image/')) return 'image';
  if (ct.startsWith('video/')) return 'video';
  if (ct.startsWith('audio/')) return 'audio';
  if (ct === 'application/pdf') return 'pdf';
  if (ct.startsWith('text/') || TEXT_TYPES.has(ct)) return 'text';
  return 'none';
}

interface MediaSource {
  url: string;
  // streamed = presigned URL the browser fetches itself; false = object URL
  // over a blob we downloaded.
  streamed: boolean;
}

interface TextSource {
  text: string;
  truncated: boolean;
}

type Zoom = 'fit' | 'full';
const ZOOMS = [
  { key: 'fit', label: 'Fit' },
  { key: 'full', label: '100%' },
] as const;

async function tryPresign(session: Session, bucket: string, key: string): Promise<string | null> {
  try {
    return (await presignObject(session, bucket, key, PRESIGN_TTL)).url;
  } catch {
    return null;
  }
}

async function loadText(session: Session, bucket: string, key: string): Promise<TextSource> {
  const blob = await getObject(session, bucket, key);
  return { text: await blob.slice(0, TEXT_LIMIT).text(), truncated: blob.size > TEXT_LIMIT };
}

export function ObjectPreview({
  session,
  bucket,
  objectKey,
  contentType,
  onDimensions,
  onError,
}: Readonly<{
  session: Session;
  bucket: string;
  objectKey: string;
  contentType: string;
  onDimensions?: (w: number, h: number) => void;
  onError: (msg: string) => void;
}>) {
  const kind = previewKind(contentType);
  const [media, setMedia] = useState<MediaSource | null>(null);
  const [text, setText] = useState<TextSource | null>(null);
  // Set when the presigned URL exists but the browser cannot load it (the
  // public host is not reachable from here); one retry through the blob path.
  const [forceBlob, setForceBlob] = useState(false);

  useEffect(() => {
    if (kind === 'none') return;
    let cancelled = false;
    let revoke: string | null = null;
    setMedia(null);
    setText(null);
    const load = async () => {
      if (kind === 'text') {
        const t = await loadText(session, bucket, objectKey);
        if (!cancelled) setText(t);
        return;
      }
      const presigned = forceBlob ? null : await tryPresign(session, bucket, objectKey);
      if (cancelled) return;
      if (presigned) {
        setMedia({ url: presigned, streamed: true });
        return;
      }
      const blob = await getObject(session, bucket, objectKey);
      if (cancelled) return;
      revoke = URL.createObjectURL(blob);
      setMedia({ url: revoke, streamed: false });
    };
    load().catch((e: unknown) => {
      if (!cancelled) onError(errorMessage(e));
    });
    return () => {
      cancelled = true;
      if (revoke) URL.revokeObjectURL(revoke);
    };
    // onError is a plain callback; re-running the fetch when it changes identity would refetch on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, bucket, objectKey, kind, forceBlob]);

  function onMediaError() {
    if (media?.streamed) setForceBlob(true);
  }

  const name = objectKey.split('/').pop() ?? objectKey;

  if (kind === 'none') {
    return (
      <Frame>
        <p className="text-sm text-ink-500">No inline preview for this type. Use Download.</p>
      </Frame>
    );
  }
  if (kind === 'text') {
    return text ? <TextPreview src={text} /> : <Frame><Loading /></Frame>;
  }
  if (!media) return <Frame><Loading /></Frame>;
  if (kind === 'image') {
    return <ImagePreview src={media} name={name} onDimensions={onDimensions} onError={onMediaError} />;
  }
  if (kind === 'pdf') {
    return (
      <Frame>
        <iframe title={name} src={media.url} className="w-full h-[560px] border border-ink-200" />
      </Frame>
    );
  }
  return <MediaPreview kind={kind} src={media} onError={onMediaError} />;
}

function Frame({ control, children }: Readonly<{ control?: ReactNode; children: ReactNode }>) {
  return (
    <section className="flex flex-col gap-2.5">
      <div className="flex items-center justify-between h-7">
        <h2 className="text-sm font-medium">Preview</h2>
        {control}
      </div>
      {children}
    </section>
  );
}

function TextPreview({ src }: Readonly<{ src: TextSource }>) {
  return (
    <Frame control={src.truncated ? <span className="text-xs text-ink-500">First 256 KB shown</span> : undefined}>
      <pre className="m-0 font-mono text-xs leading-normal border border-ink-200 px-3.5 py-3 whitespace-pre-wrap break-all h-[560px] overflow-auto">
        {src.text}
      </pre>
    </Frame>
  );
}

function ImagePreview({
  src,
  name,
  onDimensions,
  onError,
}: Readonly<{
  src: MediaSource;
  name: string;
  onDimensions?: (w: number, h: number) => void;
  onError: () => void;
}>) {
  const [zoom, setZoom] = useState<Zoom>('fit');
  const [dims, setDims] = useState<{ w: number; h: number } | null>(null);
  const fit = zoom === 'fit';
  const control = (
    <div className="flex items-center gap-3">
      {dims && <span className="text-xs text-ink-500 tabular-nums">{dims.w} × {dims.h}</span>}
      <Seg options={ZOOMS} value={zoom} onChange={setZoom} label="Zoom" />
      <IconButton
        icon="open"
        label="Open original in a new tab"
        onClick={() => window.open(src.url, '_blank', 'noopener')}
      />
    </div>
  );
  return (
    <Frame control={control}>
      <div
        className={`border border-ink-200 bg-ink-50 h-[560px] overflow-auto${fit ? ' flex items-center justify-center' : ''}`}
      >
        <img
          src={src.url}
          alt={name}
          className={fit ? 'block max-w-full max-h-full' : 'block max-w-none'}
          onLoad={(e) => {
            const { naturalWidth: w, naturalHeight: h } = e.currentTarget;
            setDims({ w, h });
            onDimensions?.(w, h);
          }}
          onError={onError}
        />
      </div>
    </Frame>
  );
}

function MediaPreview({
  kind,
  src,
  onError,
}: Readonly<{ kind: 'video' | 'audio'; src: MediaSource; onError: () => void }>) {
  const note = src.streamed ? 'Streams from the object URL · nothing is downloaded up front' : undefined;
  return (
    <Frame control={note && <span className="text-xs text-ink-500">{note}</span>}>
      {kind === 'video' ? (
        <div className="border border-ink-200 bg-ink-900 h-[560px] flex items-center justify-center">
          <video src={src.url} controls preload="metadata" className="max-w-full max-h-full" onError={onError} />
        </div>
      ) : (
        <div className="border border-ink-200 bg-ink-50 px-4 py-6">
          <audio src={src.url} controls preload="metadata" className="w-full" onError={onError} />
        </div>
      )}
    </Frame>
  );
}
