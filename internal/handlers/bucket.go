package handlers

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// HealthHandler returns a simple JSON status.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// objectsRoot is where bucket directories live. Exposed as a var for tests;
// production always uses /data/objects.
var objectsRoot = "/data/objects"

// CreateBucketHandler creates a new bucket (directory) and returns an XML
// response compatible with S3 SDK expectations. Errors flow through
// respondError so the admin surface sees JSON while SigV4 callers see XML.
func CreateBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if err := storage.ValidateBucketName(bucketName); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidBucketName",
			"Bucket name must be 3-63 chars, lowercase alphanumeric and hyphens, starting and ending alphanumeric")
		return
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)

	if fileInfo, err := os.Stat(bucketPath); err == nil && fileInfo.IsDir() {
		// BucketAlreadyOwnedByYou keeps the bespoke XML shape with BucketName
		// because the AWS SDK surfaces it to user code; we preserve the wire
		// format here rather than collapsing into respondError's generic body.
		if wantsJSON(c) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"code":       "BucketAlreadyOwnedByYou",
				"message":    "Your previous request to create the named bucket succeeded and you already own it.",
				"bucketName": bucketName,
			})
			return
		}
		c.XML(http.StatusConflict, struct {
			XMLName    xml.Name `xml:"Error"`
			Code       string   `xml:"Code"`
			Message    string   `xml:"Message"`
			BucketName string   `xml:"BucketName"`
			RequestId  string   `xml:"RequestId"`
			HostId     string   `xml:"HostId"`
		}{
			Code:       "BucketAlreadyOwnedByYou",
			Message:    "Your previous request to create the named bucket succeeded and you already own it.",
			BucketName: bucketName,
			RequestId:  middleware.RequestID(c),
		})
		return
	}

	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error creating bucket")
		return
	}

	// Honour x-amz-acl at create time so SDK callers can publish a public
	// bucket in a single round-trip. Absent header keeps the implicit
	// "private" default — no sidecar is written, matching CORS behaviour.
	if hdr := c.GetHeader("x-amz-acl"); hdr != "" {
		canned, err := storage.NormalizeCannedACL(hdr)
		if err != nil {
			respondError(c, http.StatusBadRequest, "InvalidArgument", "Unsupported x-amz-acl value")
			return
		}
		if err := storage.PutBucketACL(bucketName, &storage.BucketACL{Canned: canned}); err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError", "Error persisting bucket ACL")
			return
		}
		// Audit the ACL set at create time. Prior is always the implicit
		// "private" default since the bucket did not exist a moment ago.
		auditACLChange(c, "bucket", bucketName, "", storage.ACLPrivate, canned)
	}

	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"location": fmt.Sprintf("http://%s/%s", c.Request.Host, bucketName)})
		return
	}
	c.XML(http.StatusOK, struct {
		XMLName  xml.Name `xml:"CreateBucketResult"`
		Location string   `xml:"Location"`
	}{
		Location: fmt.Sprintf("http://%s/%s", c.Request.Host, bucketName),
	})
}

// ListBucketsHandler returns a list of buckets.
func ListBucketsHandler(c *gin.Context) {
	entries, err := os.ReadDir(objectsRoot)
	if err != nil && !os.IsNotExist(err) {
		respondError(c, http.StatusInternalServerError, "InternalError",
			fmt.Sprintf("Error listing buckets: %v", err))
		return
	}
	// A missing objectsRoot is the normal pre-first-bucket state and the
	// post-delete-all-buckets state: both must report an empty list, not 500.

	type Bucket struct {
		Name         string `xml:"Name" json:"name"`
		CreationDate string `xml:"CreationDate" json:"creationDate"`
		ACL          string `xml:"-" json:"acl,omitempty"`
	}
	var buckets []Bucket
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError",
				fmt.Sprintf("Error getting info for bucket %s: %v", entry.Name(), err))
			return
		}
		// Effective ACL is surfaced on the admin JSON view so the UI can
		// render a visibility column without a second round-trip per row.
		// XML (SigV4) callers do not see this field — S3 keeps ACL on the
		// dedicated ?acl subresource and SDKs would not parse it here.
		acl, err := storage.EffectiveBucketACL(entry.Name())
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError",
				fmt.Sprintf("Error reading ACL for bucket %s: %v", entry.Name(), err))
			return
		}
		buckets = append(buckets, Bucket{
			Name:         entry.Name(),
			CreationDate: info.ModTime().Format(time.RFC3339),
			ACL:          acl,
		})
	}

	type owner struct {
		ID          string `xml:"ID" json:"id"`
		DisplayName string `xml:"DisplayName" json:"displayName"`
	}
	// Owner reflects the authenticated caller. Auth middleware publishes the
	// storage.User on the context; we fall back to empty strings only if the
	// handler is ever reached without auth — the routers today prevent that,
	// but a nil assertion would mask a configuration error and is not worth
	// the panic risk on a response path.
	var ownerID string
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*storage.User); ok {
			ownerID = u.AccessKeyID
		}
	}
	xmlResult := struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		XMLNS   string   `xml:"xmlns,attr"`
		Owner   owner    `xml:"Owner"`
		Buckets struct {
			Bucket []Bucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}{
		XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/",
		// DisplayName is an opaque label in S3; reusing the access key keeps
		// it predictable without inventing a new user-profile field.
		Owner: owner{ID: ownerID, DisplayName: ownerID},
	}
	xmlResult.Buckets.Bucket = buckets

	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"buckets": buckets})
		return
	}
	c.XML(http.StatusOK, xmlResult)
}

// DeleteBucketHandler deletes a bucket.
func DeleteBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if bucketName == "" {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Bucket name required")
		return
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)
	if bucketPath == objectsRoot {
		respondError(c, http.StatusBadRequest, "InvalidBucketName", "Cannot delete base directory")
		return
	}

	if _, err := os.Stat(bucketPath); os.IsNotExist(err) {
		respondError(c, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		return
	}

	// Refuse to delete a non-empty bucket. The previous behaviour
	// (os.RemoveAll without a check) made data loss one misclick away —
	// a real concern for the parent-tracking deployments that hold
	// irreplaceable photos. S3 returns BucketNotEmpty for the same case;
	// matching that wire code keeps SDK semantics consistent and gives
	// the UI a code to render a meaningful confirmation.
	empty, err := bucketIsEmpty(bucketPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error reading bucket")
		return
	}
	if !empty {
		respondError(c, http.StatusConflict, "BucketNotEmpty",
			"The bucket you tried to delete is not empty. Remove all objects first.")
		return
	}

	if err := os.RemoveAll(bucketPath); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error deleting bucket")
		return
	}

	c.Status(http.StatusNoContent)
}

// bucketIsEmpty reports whether the bucket directory contains any
// real object files. Per-bucket sidecars (.cors.json, .acl.json) are
// ignored — they are server-managed state, not user-visible objects,
// and would otherwise force the operator to clear ACL/CORS before
// they could ever delete a bucket they just emptied through the UI.
// Recurses one level deep using WalkDir because S3-style keys live in
// nested subdirectories.
func bucketIsEmpty(bucketPath string) (bool, error) {
	empty := true
	err := filepath.WalkDir(bucketPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if isSidecar(name) {
			return nil
		}
		empty = false
		return fs.SkipAll
	})
	return empty, err
}

// listObjectInfo is the per-object row returned by ListObjects. Field tags
// are dual-encoded so the same struct serialises into the S3 XML wire shape
// on the SigV4 surface and the admin JSON shape on /api/s3.
type listObjectInfo struct {
	Key          string `xml:"Key" json:"key"`
	LastModified string `xml:"LastModified" json:"lastModified"`
	ETag         string `xml:"ETag" json:"etag"`
	Size         int64  `xml:"Size" json:"size"`
	StorageClass string `xml:"StorageClass" json:"storageClass"`
	// ACL is the effective canned ACL after applying bucket inheritance.
	// ACLSource is "object" when the object set its own value, "bucket"
	// when inherited, or "default" when neither was set. Surfaced only
	// on the admin JSON view; SigV4 XML omits it.
	ACL       string `xml:"-" json:"acl,omitempty"`
	ACLSource string `xml:"-" json:"aclSource,omitempty"`
}

// listCommonPrefix mirrors S3's <CommonPrefixes><Prefix>...</Prefix>...
// element. A bucket "folder" appears here when the delimiter query rolls up
// every key sharing that prefix into a single entry.
type listCommonPrefix struct {
	Prefix string `xml:"Prefix" json:"prefix"`
}

// listMaxKeys bounds how many objects+commonPrefixes a single response may
// carry. Matches the S3 default and ceiling so clients written against AWS
// do not need a behavioural override.
const listMaxKeys = 1000

// isSidecar reports whether a file name is one of our internal sidecars
// (.meta, .cors.json, .acl.json). Used at every recursion depth so a key
// like "data/.meta" is excluded just as reliably as "/data/foo.txt.meta".
func isSidecar(name string) bool {
	return strings.HasSuffix(name, ".meta") || name == ".cors.json" || name == ".acl.json"
}

// ListObjectsHandler implements S3 ListObjects / ListObjectsV2 semantics:
// recursive walk of the bucket directory, prefix scoping, delimiter rollup
// into CommonPrefixes, and continuation-token paging. The same handler is
// mounted on both the SigV4 (XML) surface and the admin (JSON) surface;
// only the response encoder differs.
func ListObjectsHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	bucketPath := filepath.Join(objectsRoot, bucketName)
	if info, err := os.Stat(bucketPath); err != nil || !info.IsDir() {
		respondError(c, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		return
	}

	q := c.Request.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	maxKeys := parseMaxKeys(q.Get("max-keys"))

	// list-type=2 selects the strict ListObjectsV2 contract (KeyCount,
	// ContinuationToken/StartAfter, no Marker). Anything else is v1, which
	// pages by Marker. The two differ only in cursor source and response
	// shape; the walk/filter/sort below is shared.
	listTypeV2 := q.Get("list-type") == "2"
	contToken := q.Get("continuation-token")
	startAfterParam := q.Get("start-after")
	var startAfter string
	switch {
	case contToken != "":
		// Opaque, server-issued cursor. Decoding never builds a filesystem
		// path — it is compared lexically only — so a forged token cannot
		// traverse out of the bucket.
		startAfter = decodeContinuation(contToken)
	case listTypeV2:
		startAfter = startAfterParam
	default:
		startAfter = q.Get("marker")
	}

	// Walk the bucket recursively. WalkDir visits in lexical order per
	// directory but interleaves nested entries with siblings, so we cannot
	// rely on traversal order for the final response — collect first, sort
	// after filtering. The cost is one pass plus a sort; acceptable at the
	// listMaxKeys=1000 ceiling we paginate to.
	type rawEntry struct {
		key  string
		path string
		mod  time.Time
		size int64
	}
	var all []rawEntry
	walkErr := filepath.WalkDir(bucketPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isSidecar(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(bucketPath, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		all = append(all, rawEntry{key: key, path: path, mod: info.ModTime(), size: info.Size()})
		return nil
	})
	if walkErr != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Error reading bucket")
		return
	}

	// Apply prefix filter, delimiter rollup, and continuation token before
	// resolving ETag/ACL so we do not pay sidecar IO for entries we will not
	// emit. A delimiter set to "/" collapses every nested object under a
	// shared prefix into one CommonPrefix entry, exactly matching S3.
	objects := make([]rawEntry, 0, len(all))
	prefixSet := make(map[string]struct{})
	for _, e := range all {
		if prefix != "" && !strings.HasPrefix(e.key, prefix) {
			continue
		}
		if delimiter != "" {
			rest := e.key[len(prefix):]
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				cp := prefix + rest[:idx+len(delimiter)]
				prefixSet[cp] = struct{}{}
				continue
			}
		}
		objects = append(objects, e)
	}

	// Sort both arms lexicographically so pagination by "last key emitted"
	// is well-defined across both objects and common prefixes.
	sort.Slice(objects, func(i, j int) bool { return objects[i].key < objects[j].key })
	commonPrefixes := make([]string, 0, len(prefixSet))
	for p := range prefixSet {
		commonPrefixes = append(commonPrefixes, p)
	}
	sort.Strings(commonPrefixes)

	// Pagination cursor: drop anything <= startAfter from BOTH arms. We
	// merge objects + commonPrefixes into a single sorted stream so a
	// single token resumes the listing deterministically.
	type emitItem struct {
		isPrefix bool
		key      string // object key or common prefix string
		entry    rawEntry
	}
	merged := make([]emitItem, 0, len(objects)+len(commonPrefixes))
	for _, e := range objects {
		merged = append(merged, emitItem{key: e.key, entry: e})
	}
	for _, p := range commonPrefixes {
		merged = append(merged, emitItem{isPrefix: true, key: p})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].key < merged[j].key })
	if startAfter != "" {
		// Linear seek is fine — len(merged) <= a few thousand in practice
		// and a binary search would obscure the invariant that pagination
		// drops everything LEX-≤ token.
		cut := 0
		for cut < len(merged) && merged[cut].key <= startAfter {
			cut++
		}
		merged = merged[cut:]
	}

	truncated := false
	var lastKey string
	if len(merged) > maxKeys {
		truncated = true
		lastKey = merged[maxKeys-1].key
		merged = merged[:maxKeys]
	}

	// Resolve ETag + ACL for the surviving objects only. ACL inheritance
	// reads the bucket sidecar once per object; for buckets without a
	// per-bucket ACL the call short-circuits to the in-memory default.
	emittedObjects := make([]listObjectInfo, 0, maxKeys)
	emittedPrefixes := make([]listCommonPrefix, 0)
	for _, it := range merged {
		if it.isPrefix {
			emittedPrefixes = append(emittedPrefixes, listCommonPrefix{Prefix: it.key})
			continue
		}
		etag, err := loadOrBackfillETag(it.entry.path)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError",
				fmt.Sprintf("Error resolving ETag for %s: %v", it.key, err))
			return
		}
		acl, aclSrc, err := storage.ResolveObjectACL(bucketName, it.entry.path)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError",
				fmt.Sprintf("Error resolving ACL for %s: %v", it.key, err))
			return
		}
		emittedObjects = append(emittedObjects, listObjectInfo{
			Key:          it.key,
			LastModified: it.entry.mod.Format(time.RFC3339),
			ETag:         etag,
			Size:         it.entry.size,
			StorageClass: "STANDARD",
			ACL:          acl,
			ACLSource:    aclSrc,
		})
	}

	page := listPage{
		bucket:         bucketName,
		prefix:         prefix,
		delimiter:      delimiter,
		maxKeys:        maxKeys,
		keyCount:       len(emittedObjects) + len(emittedPrefixes),
		truncated:      truncated,
		lastKey:        lastKey,
		objects:        emittedObjects,
		commonPrefixes: emittedPrefixes,
	}
	switch {
	case wantsJSON(c):
		respondListJSON(c, page)
	case listTypeV2:
		respondListV2(c, page, contToken, startAfterParam)
	default:
		respondListV1(c, page, startAfter)
	}
}

// listPage is the version-agnostic result of a ListObjects walk. The v1/v2
// encoders below differ only in how they expose the pagination cursor.
type listPage struct {
	bucket         string
	prefix         string
	delimiter      string
	maxKeys        int
	keyCount       int
	truncated      bool
	lastKey        string // last emitted key when truncated; "" otherwise
	objects        []listObjectInfo
	commonPrefixes []listCommonPrefix
}

// respondListJSON serves the admin (JSON) surface. The admin UI is our own
// client and pages purely on the opaque continuation token, independent of
// S3 list-type semantics.
func respondListJSON(c *gin.Context, p listPage) {
	body := gin.H{
		"name":           p.bucket,
		"prefix":         p.prefix,
		"delimiter":      p.delimiter,
		"maxKeys":        p.maxKeys,
		"keyCount":       p.keyCount,
		"isTruncated":    p.truncated,
		"contents":       p.objects,
		"commonPrefixes": p.commonPrefixes,
	}
	if p.truncated {
		body["nextContinuationToken"] = encodeContinuation(p.lastKey)
	}
	c.JSON(http.StatusOK, body)
}

// respondListV2 emits the strict ListObjectsV2 shape: KeyCount, the echoed
// request ContinuationToken/StartAfter, and an opaque NextContinuationToken.
// It never emits Marker.
func respondListV2(c *gin.Context, p listPage, contToken, startAfter string) {
	result := struct {
		XMLName               xml.Name           `xml:"ListBucketResult"`
		XMLNS                 string             `xml:"xmlns,attr"`
		Name                  string             `xml:"Name"`
		Prefix                string             `xml:"Prefix"`
		Delimiter             string             `xml:"Delimiter,omitempty"`
		MaxKeys               int                `xml:"MaxKeys"`
		KeyCount              int                `xml:"KeyCount"`
		IsTruncated           bool               `xml:"IsTruncated"`
		ContinuationToken     string             `xml:"ContinuationToken,omitempty"`
		NextContinuationToken string             `xml:"NextContinuationToken,omitempty"`
		StartAfter            string             `xml:"StartAfter,omitempty"`
		Contents              []listObjectInfo   `xml:"Contents"`
		CommonPrefixes        []listCommonPrefix `xml:"CommonPrefixes"`
	}{
		XMLNS:             "https://s3.amazonaws.com/doc/2006-03-01/",
		Name:              p.bucket,
		Prefix:            p.prefix,
		Delimiter:         p.delimiter,
		MaxKeys:           p.maxKeys,
		KeyCount:          p.keyCount,
		IsTruncated:       p.truncated,
		ContinuationToken: contToken,
		StartAfter:        startAfter,
		Contents:          p.objects,
		CommonPrefixes:    p.commonPrefixes,
	}
	if p.truncated {
		result.NextContinuationToken = encodeContinuation(p.lastKey)
	}
	c.XML(http.StatusOK, result)
}

// respondListV1 emits the legacy ListObjects shape, paging by Marker. Per S3,
// NextMarker is only returned when a delimiter is set; without one the client
// resumes from the last key it received.
func respondListV1(c *gin.Context, p listPage, marker string) {
	result := struct {
		XMLName        xml.Name           `xml:"ListBucketResult"`
		XMLNS          string             `xml:"xmlns,attr"`
		Name           string             `xml:"Name"`
		Prefix         string             `xml:"Prefix"`
		Marker         string             `xml:"Marker"`
		NextMarker     string             `xml:"NextMarker,omitempty"`
		Delimiter      string             `xml:"Delimiter,omitempty"`
		MaxKeys        int                `xml:"MaxKeys"`
		IsTruncated    bool               `xml:"IsTruncated"`
		Contents       []listObjectInfo   `xml:"Contents"`
		CommonPrefixes []listCommonPrefix `xml:"CommonPrefixes"`
	}{
		XMLNS:          "https://s3.amazonaws.com/doc/2006-03-01/",
		Name:           p.bucket,
		Prefix:         p.prefix,
		Marker:         marker,
		Delimiter:      p.delimiter,
		MaxKeys:        p.maxKeys,
		IsTruncated:    p.truncated,
		Contents:       p.objects,
		CommonPrefixes: p.commonPrefixes,
	}
	if p.truncated && p.delimiter != "" {
		result.NextMarker = p.lastKey
	}
	c.XML(http.StatusOK, result)
}

// parseMaxKeys clamps the user-supplied max-keys to the legal [1, listMaxKeys]
// range. Malformed input falls back to the ceiling instead of erroring; AWS
// SDKs occasionally pass empty strings on default and we want them to work.
func parseMaxKeys(raw string) int {
	if raw == "" {
		return listMaxKeys
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return listMaxKeys
	}
	if n > listMaxKeys {
		return listMaxKeys
	}
	return n
}

// encodeContinuation/decodeContinuation wrap pagination tokens in URL-safe
// base64 so they survive query-string round-trips and so the on-the-wire
// token does not leak the raw key path. The token is opaque to clients.
func encodeContinuation(key string) string {
	return base64.URLEncoding.EncodeToString([]byte(key))
}

func decodeContinuation(token string) string {
	if token == "" {
		return ""
	}
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		// A malformed token can either error or be ignored; ignoring is
		// kinder to clients that recycled an outdated token and matches
		// what AWS does in practice.
		return ""
	}
	return string(b)
}

// HeadBucketHandler checks if a bucket exists and returns 200/404 with no body
// per the S3 HeadBucket contract. Auth/ACL are already enforced by middleware.
func HeadBucketHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	if bucketName == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	bucketPath := filepath.Join(objectsRoot, bucketName)
	fileInfo, err := os.Stat(bucketPath)
	if os.IsNotExist(err) {
		c.Status(http.StatusNotFound)
		return
	} else if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if !fileInfo.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Status(http.StatusOK)
}
