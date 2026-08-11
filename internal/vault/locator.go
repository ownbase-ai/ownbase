package vault

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ownbase/ownbase/internal/objstore"
)

// LocatorFile is the JSON file under ~/.ownbase that records where the vault
// lives and how to reach it. Replaces the plain-path PointerFile for remote
// stores; the pointer file is still read for backward compatibility.
const LocatorFile = "locator"

// DefaultObjectKey is the object key used inside a vault bucket when the user
// does not specify one.
const DefaultObjectKey = "ownbase/vault/ownbase.kdbx"

// Locator describes how to open the vault Store. Kind selects the backend.
//
// Secrets (SecretAccessKey) live in this file at mode 0600. The recovery
// string is a portable encoding of the same document.
type Locator struct {
	// Kind is "file" or "s3".
	Kind string `json:"kind"`

	// File fields (kind == "file").
	Path string `json:"path,omitempty"`

	// S3 fields (kind == "s3").
	Endpoint        string `json:"endpoint,omitempty"`
	Region          string `json:"region,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	Key             string `json:"key,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	PathStyle       bool   `json:"path_style,omitempty"`

	// CredsFingerprint is a non-secret stamp of the storage credentials at
	// the time the recovery string was last printed. Compared on unlock so a
	// rotated key is noticed before a disaster, not during one.
	CredsFingerprint string `json:"creds_fingerprint,omitempty"`
}

// Kind values.
const (
	LocatorKindFile = "file"
	LocatorKindS3   = "s3"
)

// Validate checks required fields for the locator's kind.
func (l Locator) Validate() error {
	switch l.Kind {
	case LocatorKindFile:
		if strings.TrimSpace(l.Path) == "" {
			return errors.New("file locator requires path")
		}
	case LocatorKindS3:
		if strings.TrimSpace(l.Bucket) == "" {
			return errors.New("s3 locator requires bucket")
		}
		if strings.TrimSpace(l.Region) == "" {
			return errors.New("s3 locator requires region")
		}
		if strings.TrimSpace(l.AccessKeyID) == "" || strings.TrimSpace(l.SecretAccessKey) == "" {
			return errors.New("s3 locator requires access_key_id and secret_access_key")
		}
		if strings.TrimSpace(l.Key) == "" {
			return errors.New("s3 locator requires key")
		}
	case "":
		return errors.New("locator kind is required (file or s3)")
	default:
		return fmt.Errorf("unknown locator kind %q", l.Kind)
	}
	return nil
}

// Location returns a human-readable, secret-free identifier for display.
func (l Locator) Location() string {
	switch l.Kind {
	case LocatorKindFile:
		return l.Path
	case LocatorKindS3:
		ep := l.Endpoint
		if ep == "" {
			ep = "s3://" + l.Region
		}
		return fmt.Sprintf("%s/%s/%s", strings.TrimRight(ep, "/"), l.Bucket, l.Key)
	default:
		return l.Kind
	}
}

// Fingerprint returns a short non-secret stamp of the credentials so a stale
// recovery string can be detected. Empty for file locators.
func (l Locator) Fingerprint() string {
	if l.Kind != LocatorKindS3 {
		return ""
	}
	mac := hmac.New(sha256.New, []byte("ownbase-locator-v1"))
	_, _ = mac.Write([]byte(l.Endpoint + "\n" + l.Region + "\n" + l.Bucket + "\n" + l.Key + "\n" + l.AccessKeyID + "\n" + l.SecretAccessKey))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// OpenStore builds the Store this locator describes.
func (l Locator) OpenStore() (Store, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	switch l.Kind {
	case LocatorKindFile:
		return NewFileStore(l.Path), nil
	case LocatorKindS3:
		client, err := objstore.New(objstore.Config{
			Endpoint:        l.Endpoint,
			Region:          l.Region,
			Bucket:          l.Bucket,
			AccessKeyID:     l.AccessKeyID,
			SecretAccessKey: l.SecretAccessKey,
			PathStyle:       l.PathStyle,
		})
		if err != nil {
			return nil, err
		}
		s3 := NewS3Store(client, l.Key)
		s3.SetLocation(l.Location())
		// Local ciphertext cache so reads still work offline after at least
		// one successful fetch. Writes never go to the cache alone.
		cacheDir, err := StatePath("cache")
		if err != nil {
			return s3, nil // cache is best-effort
		}
		return NewCachingStore(s3, cacheDir), nil
	default:
		return nil, fmt.Errorf("unknown locator kind %q", l.Kind)
	}
}

// LoadLocator reads ~/.ownbase/locator. Falls back to the legacy plain-path
// pointer file so existing installs keep working.
func LoadLocator() (Locator, error) {
	if p := strings.TrimSpace(os.Getenv(PathEnv)); p != "" {
		return locatorFromEnv(p)
	}

	locPath, err := StatePath(LocatorFile)
	if err != nil {
		return Locator{}, err
	}
	data, err := os.ReadFile(locPath)
	if err == nil {
		var loc Locator
		if err := json.Unmarshal(data, &loc); err != nil {
			return Locator{}, fmt.Errorf("parse %s: %w", locPath, err)
		}
		if err := loc.Validate(); err != nil {
			return Locator{}, fmt.Errorf("%s: %w", locPath, err)
		}
		return loc, nil
	}
	if !os.IsNotExist(err) {
		return Locator{}, fmt.Errorf("read %s: %w", locPath, err)
	}

	// Legacy pointer file (pre-locator installs).
	path, err := resolveLegacyPointer()
	if err != nil {
		return Locator{}, err
	}
	return Locator{Kind: LocatorKindFile, Path: path}, nil
}

// SaveLocator writes the locator file (mode 0600) and, for file locators,
// keeps the legacy pointer file in sync so older tools still find the path.
func SaveLocator(loc Locator) error {
	if err := loc.Validate(); err != nil {
		return err
	}
	loc.CredsFingerprint = loc.Fingerprint()

	locPath, err := StatePath(LocatorFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(locPath), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(locPath), err)
	}
	data, err := json.MarshalIndent(loc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(locPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", locPath, err)
	}

	if loc.Kind == LocatorKindFile {
		if err := writePointer(loc.Path); err != nil {
			return err
		}
	}
	return nil
}

// ResolveStore returns the Store for the configured vault location.
func ResolveStore() (Store, error) {
	loc, err := LoadLocator()
	if err != nil {
		return nil, err
	}
	return loc.OpenStore()
}

func locatorFromEnv(p string) (Locator, error) {
	p = ExpandTilde(p)
	// s3://bucket/key is a convenience form; credentials must still come from
	// the locator file or explicit flags — env alone cannot carry secrets
	// safely. Treat as file path otherwise.
	if strings.HasPrefix(p, "s3://") {
		return Locator{}, errors.New("$OWNBASE_VAULT=s3://... is not enough on its own — credentials live in ~/.ownbase/locator; use 'ownbasectl vault open --recovery' on a fresh machine")
	}
	abs, err := NormalizePath(p)
	if err != nil {
		return Locator{}, err
	}
	return Locator{Kind: LocatorKindFile, Path: abs}, nil
}

// ---------------------------------------------------------------------------
// Recovery string
// ---------------------------------------------------------------------------

const recoveryPrefix = "ownbase-recovery-v1:"

// EncodeRecovery returns a portable recovery string for loc. The string holds
// everything needed to fetch the vault except the master password.
func EncodeRecovery(loc Locator) (string, error) {
	if err := loc.Validate(); err != nil {
		return "", err
	}
	loc.CredsFingerprint = loc.Fingerprint()
	raw, err := json.Marshal(loc)
	if err != nil {
		return "", err
	}
	// Append a checksum of the payload so typos are caught before a network call.
	sum := sha256.Sum256(raw)
	payload := append(raw, sum[:4]...)
	return recoveryPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeRecovery parses a recovery string into a Locator.
func DecodeRecovery(s string) (Locator, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, recoveryPrefix) {
		return Locator{}, errors.New("not an ownbase recovery string (expected ownbase-recovery-v1:...)")
	}
	enc := strings.TrimPrefix(s, recoveryPrefix)
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return Locator{}, fmt.Errorf("decode recovery string: %w", err)
	}
	if len(payload) < 5 {
		return Locator{}, errors.New("recovery string is truncated")
	}
	raw, sum := payload[:len(payload)-4], payload[len(payload)-4:]
	want := sha256.Sum256(raw)
	if !hmac.Equal(sum, want[:4]) {
		return Locator{}, errors.New("recovery string checksum failed — it may be truncated or mistyped")
	}
	var loc Locator
	if err := json.Unmarshal(raw, &loc); err != nil {
		return Locator{}, fmt.Errorf("parse recovery string: %w", err)
	}
	if err := loc.Validate(); err != nil {
		return Locator{}, err
	}
	return loc, nil
}
