// Package objstore is a minimal S3-compatible object client: GET, PUT, and
// HEAD only, signed with AWS Signature Version 4. No SDK dependency — the
// vault is kilobytes and needs conditional writes (If-Match / If-None-Match),
// not the rest of the S3 API.
//
// Works against AWS S3, Cloudflare R2, Backblaze B2's S3 endpoint, MinIO,
// Wasabi, and anything else that speaks SigV4 + the three verbs above.
package objstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrNotFound reports that the object key does not exist.
var ErrNotFound = errors.New("object not found")

// ErrPreconditionFailed reports that a conditional header (If-Match /
// If-None-Match) was not satisfied — the CAS lost.
var ErrPreconditionFailed = errors.New("precondition failed")

// Config holds static credentials and addressing for one bucket.
type Config struct {
	// Endpoint is the S3 API base URL, e.g. "https://s3.us-east-1.amazonaws.com"
	// or "https://<account>.r2.cloudflarestorage.com". Empty defaults to the
	// AWS virtual-host endpoint for Region.
	Endpoint string
	// Region is the AWS region (or "auto" for R2). Required for SigV4.
	Region string
	// Bucket is the bucket name.
	Bucket string
	// AccessKeyID and SecretAccessKey are static credentials.
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle forces path-style URLs (/{bucket}/{key}) instead of
	// virtual-hosted ({bucket}.endpoint/{key}). Required for MinIO and some
	// self-hosted endpoints.
	PathStyle bool
	// HTTPClient overrides the HTTP client. Nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Client talks to one bucket.
type Client struct {
	cfg        Config
	http       *http.Client
	endpoint   *url.URL
	pathStyle  bool
	now        func() time.Time // overridable in tests
	signerHost string           // host header value used in signing
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("objstore: bucket is required")
	}
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, errors.New("objstore: region is required")
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, errors.New("objstore: access key id and secret access key are required")
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", cfg.Region)
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("objstore: invalid endpoint %q", cfg.Endpoint)
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		cfg:        cfg,
		http:       hc,
		endpoint:   u,
		pathStyle:  cfg.PathStyle,
		now:        time.Now,
		signerHost: u.Host,
	}, nil
}

// Get fetches key. Returns ErrNotFound when absent.
func (c *Client) Get(ctx context.Context, key string) (body []byte, etag string, err error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", fmt.Errorf("objstore: read body: %w", err)
		}
		return data, cleanETag(resp.Header.Get("ETag")), nil
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, key)
	default:
		return nil, "", c.httpError("GET", key, resp)
	}
}

// Head returns the ETag for key without the body. Returns ErrNotFound when absent.
func (c *Client) Head(ctx context.Context, key string) (etag string, err error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return cleanETag(resp.Header.Get("ETag")), nil
	case http.StatusNotFound:
		return "", fmt.Errorf("%w: %s", ErrNotFound, key)
	default:
		return "", c.httpError("HEAD", key, resp)
	}
}

// PutOptions controls conditional writes.
type PutOptions struct {
	// IfMatch is an ETag; the put succeeds only when the live object matches.
	IfMatch string
	// IfNoneMatch, when "*", succeeds only when no object exists (create).
	IfNoneMatch string
}

// Put uploads body at key. Returns the new ETag.
func (c *Client) Put(ctx context.Context, key string, body []byte, opts PutOptions) (etag string, err error) {
	hdrs := http.Header{}
	if opts.IfMatch != "" {
		hdrs.Set("If-Match", quoteETag(opts.IfMatch))
	}
	if opts.IfNoneMatch != "" {
		hdrs.Set("If-None-Match", opts.IfNoneMatch)
	}
	resp, err := c.do(ctx, http.MethodPut, key, body, hdrs)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return cleanETag(resp.Header.Get("ETag")), nil
	case http.StatusPreconditionFailed, http.StatusConflict:
		return "", fmt.Errorf("%w: %s", ErrPreconditionFailed, key)
	case http.StatusNotFound:
		// Some providers return 404 when If-Match targets a missing key.
		return "", fmt.Errorf("%w: %s", ErrPreconditionFailed, key)
	default:
		return "", c.httpError("PUT", key, resp)
	}
}

func (c *Client) do(ctx context.Context, method, key string, body []byte, extra http.Header) (*http.Response, error) {
	key = strings.TrimPrefix(key, "/")
	if key == "" {
		return nil, errors.New("objstore: object key is required")
	}

	reqURL := c.objectURL(key)
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	payloadHash := sha256Hex(body)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := c.sign(req, payloadHash); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("objstore: %s %s: %w", method, key, err)
	}
	return resp, nil
}

func (c *Client) objectURL(key string) string {
	// Encode each path segment so slashes in the key remain separators.
	encoded := encodeKey(key)
	if c.pathStyle {
		return fmt.Sprintf("%s://%s/%s/%s", c.endpoint.Scheme, c.endpoint.Host, c.cfg.Bucket, encoded)
	}
	// Virtual-hosted-style: bucket as subdomain.
	host := c.cfg.Bucket + "." + c.endpoint.Host
	return fmt.Sprintf("%s://%s/%s", c.endpoint.Scheme, host, encoded)
}

func (c *Client) requestHost(key string) string {
	if c.pathStyle {
		return c.endpoint.Host
	}
	return c.cfg.Bucket + "." + c.endpoint.Host
}

func (c *Client) sign(req *http.Request, payloadHash string) error {
	now := c.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Host = c.requestHost(strings.TrimPrefix(req.URL.Path, "/"))
	// For virtual-hosted the URL host already includes the bucket; for
	// path-style it is the endpoint host. Force the Host header to match.
	if c.pathStyle {
		req.Host = c.endpoint.Host
	} else {
		req.Host = c.cfg.Bucket + "." + c.endpoint.Host
	}
	req.Header.Set("Host", req.Host)

	// Canonical request.
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	// Query string (none for our verbs).
	canonicalQuery := ""

	// Signed headers: host, x-amz-* and any we set that must be signed.
	headers := map[string]string{
		"host":                 req.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if v := req.Header.Get("Content-Type"); v != "" {
		headers["content-type"] = v
	}
	if v := req.Header.Get("If-Match"); v != "" {
		headers["if-match"] = v
	}
	if v := req.Header.Get("If-None-Match"); v != "" {
		headers["if-none-match"] = v
	}

	var headerNames []string
	for k := range headers {
		headerNames = append(headerNames, k)
	}
	sort.Strings(headerNames)
	var canonicalHeaders strings.Builder
	for _, k := range headerNames {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers[k]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerNames, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, c.cfg.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(c.cfg.SecretAccessKey, dateStamp, c.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKeyID, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func (c *Client) httpError(method, key string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("objstore: %s %s: %s", method, key, msg)
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(data []byte) string {
	if data == nil {
		data = []byte{}
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// encodeKey percent-encodes each path segment per AWS rules (encode everything
// except unreserved characters; keep slashes as separators).
func encodeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = uriEncode(p, false)
	}
	return strings.Join(parts, "/")
}

// uriEncode implements AWS SigV4 URI encoding.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else if c == '/' && !encodeSlash {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func cleanETag(etag string) string {
	return strings.Trim(etag, `"`)
}

func quoteETag(etag string) string {
	etag = cleanETag(etag)
	if etag == "" {
		return ""
	}
	return `"` + etag + `"`
}
