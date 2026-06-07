package middleware

import "testing"

func TestS3ActivityByBucket_AggregatesOpsAndBytes(t *testing.T) {
	// Unique bucket so the cumulative global registry can't bleed across tests.
	const b = "activity-agg-bkt"
	RecordObjectUpload(b, 100)
	RecordObjectUpload(b, 50)
	RecordObjectDownload(b, 200)
	RecordObjectDelete(b)

	got := S3ActivityByBucket()[b]
	if got == nil {
		t.Fatal("bucket missing from activity rollup")
	}
	if got.Uploads != 2 {
		t.Fatalf("uploads = %v, want 2", got.Uploads)
	}
	if got.Downloads != 1 {
		t.Fatalf("downloads = %v, want 1", got.Downloads)
	}
	if got.Deletes != 1 {
		t.Fatalf("deletes = %v, want 1", got.Deletes)
	}
	if got.BytesIn != 150 {
		t.Fatalf("bytesIn = %v, want 150", got.BytesIn)
	}
	if got.BytesOut != 200 {
		t.Fatalf("bytesOut = %v, want 200", got.BytesOut)
	}
}

func TestMultipartInProgress_ReadsGauge(t *testing.T) {
	MultipartUploadsInProgress.Set(4)
	t.Cleanup(func() { MultipartUploadsInProgress.Set(0) })
	if got := MultipartInProgress(); got != 4 {
		t.Fatalf("multipart gauge = %v, want 4", got)
	}
}
