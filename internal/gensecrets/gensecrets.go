// Package gensecrets fills in the secrets a config declares via
// generated_secrets: but does not — and cannot — contain.
//
// Some credentials have no business being authored by a human. An SSH keypair
// cannot be expressed in YAML at all, and a database password that an operator
// invents is a password they are tempted to reuse. So ownbase.yaml names the
// keys and leaves the values to the Base: on reconcile, Ensure generates
// whatever is missing and stores it in the same age-encrypted per-service files
// that `ownbasectl secrets set` writes, from which internal/podman injects it
// into the container as an environment variable.
//
// Two properties make this safe to run on every reconcile tick:
//
//   - It only ever fills gaps. A destination key that already has a value is
//     left alone, so restarts do not churn credentials and an operator can
//     always override a generated value by setting it by hand.
//   - Generation happens on the Base. A private key never crosses the network
//     nor touches the operator's disk, and a rebuilt Base regenerates what it
//     needs without anyone having to remember what was there before.
package gensecrets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
)

// Config locates the secret store Ensure reads and writes.
type Config struct {
	// SecretsDir holds the age-encrypted per-service files
	// (<SecretsDir>/<service>.yaml.age). Empty means
	// /opt/ownbase/secrets.
	SecretsDir string

	// AgeKeyPath is the Base's age private key. Empty means
	// secrets.DefaultKeyPath.
	AgeKeyPath string
}

// DefaultSecretsDir is the conventional location of the age-encrypted
// per-service secrets files.
const DefaultSecretsDir = "/opt/ownbase/secrets"

func (c Config) secretsDir() string {
	if c.SecretsDir != "" {
		return c.SecretsDir
	}
	return DefaultSecretsDir
}

func (c Config) custody() secrets.FileKeyCustody {
	return secrets.FileKeyCustody{Path: c.AgeKeyPath}
}

// Result reports what Ensure did, for logging. Only key names appear here —
// never values.
type Result struct {
	// Generated lists the destinations that were filled, as
	// "service/KEY", sorted.
	Generated []string
}

// Ensure generates every declared secret whose destination key is still
// empty, and writes the results into the affected services' secrets files.
//
// It returns the destinations it filled. When nothing was missing, Generated
// is empty and no file is rewritten, so callers can treat a zero Result as
// "nothing to do" and stay quiet on the happy path.
func Ensure(cfg *schema.OwnbaseConfig, opts Config) (Result, error) {
	if cfg == nil {
		return Result{}, nil
	}

	// Nothing to do (and, importantly, no reason to require an age key on a
	// Base that declares no generated secrets).
	if !anyDeclared(cfg) {
		return Result{}, nil
	}

	// Read every service's existing secrets up front. A keypair writes its
	// two halves to two different services, so we cannot decide what is
	// missing one service at a time.
	existing := make(map[string]map[string]string)
	load := func(service string) (map[string]string, error) {
		if vals, ok := existing[service]; ok {
			return vals, nil
		}
		vals, err := secrets.IssueMap(opts.custody(), opts.fileFor(service))
		if err != nil {
			return nil, err
		}
		if vals == nil {
			vals = map[string]string{}
		}
		existing[service] = vals
		return vals, nil
	}

	// Accumulate writes per service so a keypair's two halves, or two
	// declarations targeting the same service, cost one file rewrite.
	pending := make(map[string]map[string]string)
	record := func(dest schema.SecretDest, value string) {
		if pending[dest.Service] == nil {
			pending[dest.Service] = map[string]string{}
		}
		pending[dest.Service][dest.Key] = value
		existing[dest.Service][dest.Key] = value
	}

	var result Result

	// Sorted service order keeps logs and tests deterministic.
	for _, svcName := range sortedKeys(cfg.Services) {
		for i, decl := range cfg.Services[svcName].GeneratedSecrets {
			dests, err := resolveDests(svcName, decl, load)
			if err != nil {
				return result, fmt.Errorf("service %q: generated_secrets[%d]: %w", svcName, i, err)
			}

			// All-or-nothing per declaration: a keypair whose halves were
			// generated in separate passes would not match, so if either half
			// is present we leave the whole declaration alone and let the
			// operator sort it out rather than silently replacing a key that
			// something may already trust.
			missing := 0
			for _, d := range dests {
				if existing[d.Service][d.Key] == "" {
					missing++
				}
			}
			if missing == 0 || missing != len(dests) {
				continue
			}

			values, err := generate(svcName, decl)
			if err != nil {
				return result, fmt.Errorf("service %q: generated_secrets[%d]: %w", svcName, i, err)
			}
			for _, d := range dests {
				v, ok := values[d.Role]
				if !ok {
					return result, fmt.Errorf("service %q: generated_secrets[%d]: no %s value produced for type %q",
						svcName, i, d.Role, decl.Type)
				}
				record(d.SecretDest, v)
				result.Generated = append(result.Generated, d.Service+"/"+d.Key)
			}
		}
	}

	if len(result.Generated) == 0 {
		return result, nil
	}

	for _, service := range sortedKeys(pending) {
		if err := opts.write(service, existing[service]); err != nil {
			return result, fmt.Errorf("store generated secrets for %q: %w", service, err)
		}
	}

	sort.Strings(result.Generated)
	return result, nil
}

// role identifies which half of a declaration a destination receives.
type role string

const (
	rolePassword   role = "password"
	rolePublicKey  role = "public key"
	rolePrivateKey role = "private key"
)

// resolvedDest is a destination plus the role of the value it receives.
type resolvedDest struct {
	schema.SecretDest
	Role role
}

// resolveDests maps a declaration to concrete (service, key, role) triples,
// substituting the declaring service for bare keys and priming the existing-
// values cache for every service involved.
func resolveDests(svcName string, decl schema.GeneratedSecretDecl, load func(string) (map[string]string, error)) ([]resolvedDest, error) {
	var out []resolvedDest
	add := func(spec string, r role) error {
		if spec == "" {
			return nil
		}
		d := schema.ParseSecretDest(spec)
		if d.Service == "" {
			d.Service = svcName
		}
		if _, err := load(d.Service); err != nil {
			return err
		}
		out = append(out, resolvedDest{SecretDest: d, Role: r})
		return nil
	}

	switch decl.Type {
	case schema.GeneratedSecretPassword:
		if err := add(decl.Key, rolePassword); err != nil {
			return nil, err
		}
	case schema.GeneratedSecretSSHEd25519:
		if err := add(decl.PublicKey, rolePublicKey); err != nil {
			return nil, err
		}
		if err := add(decl.PrivateKey, rolePrivateKey); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown type %q", decl.Type)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no destinations declared")
	}
	return out, nil
}

// generate produces the value for each role in a declaration.
func generate(svcName string, decl schema.GeneratedSecretDecl) (map[role]string, error) {
	switch decl.Type {
	case schema.GeneratedSecretPassword:
		pw, err := secrets.GeneratePassword(decl.EffectiveLength())
		if err != nil {
			return nil, err
		}
		return map[role]string{rolePassword: pw}, nil

	case schema.GeneratedSecretSSHEd25519:
		pair, err := secrets.GenerateSSHEd25519("ownbase-" + svcName)
		if err != nil {
			return nil, err
		}
		priv := pair.PrivatePEM
		if decl.Base64Private() {
			priv = pair.PrivateBase64()
		}
		return map[role]string{
			rolePublicKey:  pair.PublicAuthorizedKey,
			rolePrivateKey: priv,
		}, nil
	}
	return nil, fmt.Errorf("unknown type %q", decl.Type)
}

func (c Config) fileFor(service string) string {
	return filepath.Join(c.secretsDir(), service+".yaml.age")
}

// write encrypts values to the Base's age recipient and replaces the
// service's secrets file. Values are the full merged set, not a delta.
func (c Config) write(service string, values map[string]string) error {
	id, err := c.custody().LoadIdentity()
	if err != nil {
		return err
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.secretsDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.fileFor(service), ciphertext, 0o600)
}

func anyDeclared(cfg *schema.OwnbaseConfig) bool {
	for _, svc := range cfg.Services {
		if len(svc.GeneratedSecrets) > 0 {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
