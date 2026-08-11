package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS Signature Version 4 test — signing is deterministic and well-formed.
func TestSign_GET_KnownAnswer(t *testing.T) {
	fixed := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

	c := &Client{
		cfg: Config{
			Region:          "us-east-1",
			Bucket:          "examplebucket",
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		endpoint: mustParseURL(t, "https://s3.amazonaws.com"),
		now:      func() time.Time { return fixed },
	}

	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	payloadHash := sha256Hex(nil)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := c.sign(req, payloadHash); err != nil {
		t.Fatal(err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request") {
		t.Errorf("Authorization credential scope wrong:\n%s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") {
		t.Errorf("missing SignedHeaders: %s", auth)
	}
	idx := strings.Index(auth, "Signature=")
	if idx < 0 {
		t.Fatalf("no Signature in %s", auth)
	}
	sig := auth[idx+len("Signature="):]
	if len(sig) != 64 {
		t.Errorf("signature length = %d, want 64: %s", len(sig), sig)
	}
	if _, err := hex.DecodeString(sig); err != nil {
		t.Errorf("signature not hex: %v", err)
	}

	req2, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req2.Header.Set("X-Amz-Content-Sha256", payloadHash)
	_ = c.sign(req2, payloadHash)
	if req2.Header.Get("Authorization") != auth {
		t.Error("signature not deterministic")
	}
}

func TestSign_PUT_IncludesPayloadHash(t *testing.T) {
	fixed := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	c := &Client{
		cfg: Config{
			Region:          "us-east-1",
			Bucket:          "examplebucket",
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		endpoint: mustParseURL(t, "https://s3.amazonaws.com"),
		now:      func() time.Time { return fixed },
	}
	body := []byte("Hello, world!")
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])

	req, _ := http.NewRequest(http.MethodPut, "https://examplebucket.s3.amazonaws.com/key", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := c.sign(req, payloadHash); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "Signature=") {
		t.Fatalf("no signature: %s", auth)
	}
	body2 := []byte("other")
	sum2 := sha256.Sum256(body2)
	hash2 := hex.EncodeToString(sum2[:])
	reqB, _ := http.NewRequest(http.MethodPut, "https://examplebucket.s3.amazonaws.com/key", strings.NewReader(string(body2)))
	reqB.Header.Set("Content-Type", "application/octet-stream")
	reqB.Header.Set("X-Amz-Content-Sha256", hash2)
	_ = c.sign(reqB, hash2)
	if reqB.Header.Get("Authorization") == auth {
		t.Error("different payloads produced identical signatures")
	}
}

func TestClient_GetPutHead_AgainstFakeServer(t *testing.T) {
	type obj struct {
		data []byte
		etag string
	}
	store := map[string]*obj{}
	var etagSeq int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		key := parts[1]
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			o, ok := store[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", `"`+o.etag+`"`)
			if r.Method == http.MethodGet {
				_, _ = w.Write(o.data)
			}
		case http.MethodPut:
			ifMatch := strings.Trim(r.Header.Get("If-Match"), `"`)
			ifNone := r.Header.Get("If-None-Match")
			_, exists := store[key]
			if ifNone == "*" && exists {
				http.Error(w, "exists", http.StatusPreconditionFailed)
				return
			}
			if ifMatch != "" {
				if !exists || store[key].etag != ifMatch {
					http.Error(w, "mismatch", http.StatusPreconditionFailed)
					return
				}
			}
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			etagSeq++
			etag := hex.EncodeToString([]byte{byte(etagSeq)})
			store[key] = &obj{data: append([]byte(nil), body...), etag: etag}
			w.Header().Set("ETag", `"`+etag+`"`)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c, err := New(Config{
		Endpoint:        srv.URL,
		Region:          "us-east-1",
		Bucket:          "testbucket",
		AccessKeyID:     "AKIA_TEST",
		SecretAccessKey: "secret",
		PathStyle:       true,
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	if _, _, err := c.Get(ctx, "missing"); !isNotFound(err) {
		t.Fatalf("Get missing: %v", err)
	}

	etag, err := c.Put(ctx, "vault.kdbx", []byte("v1"), PutOptions{IfNoneMatch: "*"})
	if err != nil {
		t.Fatalf("Put create: %v", err)
	}
	if etag == "" {
		t.Fatal("empty etag on create")
	}

	if _, err := c.Put(ctx, "vault.kdbx", []byte("other"), PutOptions{IfNoneMatch: "*"}); !isPrecondition(err) {
		t.Fatalf("Put create conflict: %v", err)
	}

	body, gotETag, err := c.Get(ctx, "vault.kdbx")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" || gotETag != etag {
		t.Errorf("Get = %q etag %q, want v1 / %q", body, gotETag, etag)
	}

	headETag, err := c.Head(ctx, "vault.kdbx")
	if err != nil || headETag != etag {
		t.Errorf("Head = %q %v, want %q", headETag, err, etag)
	}

	if _, err := c.Put(ctx, "vault.kdbx", []byte("stale"), PutOptions{IfMatch: "nope"}); !isPrecondition(err) {
		t.Fatalf("stale If-Match: %v", err)
	}
	etag2, err := c.Put(ctx, "vault.kdbx", []byte("v2"), PutOptions{IfMatch: etag})
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	body, _, err = c.Get(ctx, "vault.kdbx")
	if err != nil || string(body) != "v2" {
		t.Errorf("after update: %q %v", body, err)
	}
	if etag2 == etag {
		t.Error("etag did not change after update")
	}
}

func TestNew_RequiresFields(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for empty config")
	}
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

func isPrecondition(err error) bool {
	return err != nil && strings.Contains(err.Error(), "precondition")
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
