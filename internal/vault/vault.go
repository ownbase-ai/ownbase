// Package vault stores everything ownbasectl needs to reach a Base — host,
// SSH identity, API token, config-repo pointer — in a single KDBX 4 file the
// user chooses the location of.
//
// Why KDBX and not a file of our own: the credentials that reach a Base are
// the most valuable thing on the operator's machine, and a plain YAML file in
// the home directory is readable by every process the user runs. KDBX is
// encrypted with a master password, and it is an open format with a decade of
// third-party tooling — KeePassXC, KeePassium, KeePass2Android all open this
// file. That is the escape hatch that keeps the user an owner: if OwnBase
// disappears tomorrow, the vault is still readable with software we do not
// control.
//
// Layout inside the file, chosen so it reads sensibly in KeePassXC:
//
//	OwnBase/
//	  Bases/
//	    mybase          one entry per Base
//	      Title         the Base name
//	      URL           host
//	      UserName      SSH login user
//	      Password      API bearer token          (protected)
//	      PrivateKey    owner SSH key, OpenSSH PEM (protected)
//	      PublicKey     authorized_keys line
//	      SSHPort, APIPort, ConfigRepoURL, ConfigRef, LocalVM
//
// The private half of the owner SSH key lives here and nowhere else. It is
// handed to callers as an in-memory ssh.Signer and never written to disk in
// plaintext (see internal/agentd, which serves it over the ssh-agent
// protocol).
package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/tobischo/gokeepasslib/v3"
	w "github.com/tobischo/gokeepasslib/v3/wrappers"
	"golang.org/x/crypto/ssh"
)

// Group names inside the KDBX file.
const (
	rootGroupName  = "OwnBase"
	basesGroupName = "Bases"
)

// Entry field keys. Title/UserName/Password/URL are the standard KeePass
// fields so a Base entry is legible in any KDBX client; the rest are custom
// string fields.
const (
	fieldTitle         = "Title"
	fieldUserName      = "UserName"
	fieldPassword      = "Password"
	fieldURL           = "URL"
	fieldNotes         = "Notes"
	fieldPrivateKey    = "PrivateKey"
	fieldPublicKey     = "PublicKey"
	fieldSSHPort       = "SSHPort"
	fieldAPIPort       = "APIPort"
	fieldConfigRepoURL = "ConfigRepoURL"
	fieldConfigRef     = "ConfigRef"
	fieldLocalVM       = "LocalVM"
)

// entryNotes explains the entry to anyone who opens the vault in a KDBX
// client rather than through ownbasectl.
const entryNotes = "OwnBase Base profile. Managed by ownbasectl; edit with " +
	"'ownbasectl adopt' or 'ownbasectl config set' rather than by hand."

// Defaults applied when a profile leaves a field unset.
const (
	// DefaultSSHUser is the login user assumed when a profile has none.
	DefaultSSHUser = "ubuntu"
	// DefaultAPIPort is the port the daemon's status API listens on.
	DefaultAPIPort = 7070
	// DefaultSSHPort is the standard SSH port.
	DefaultSSHPort = 22
	// DefaultConfigRef is the config-repo branch used when none is set.
	DefaultConfigRef = "main"
)

// ErrWrongPassword reports a master password that did not open the vault. It
// is distinguished from I/O errors so callers can re-prompt instead of
// bailing out.
var ErrWrongPassword = errors.New("wrong master password (or the vault file is corrupt)")

// ErrNotFound reports a Base that has no entry in the vault.
var ErrNotFound = errors.New("Base not found in the vault")

// Profile holds everything needed to reach one Base.
type Profile struct {
	// Host is the SSH hostname or IP address of the Base.
	Host string `json:"host"`
	// SSHUser is the login user for SSH.
	SSHUser string `json:"ssh_user,omitempty"`
	// SSHPort is the SSH server port.
	SSHPort int `json:"ssh_port,omitempty"`
	// APIPort is the loopback port the daemon API listens on.
	APIPort int `json:"api_port,omitempty"`
	// Token is the Bearer token for the daemon API.
	Token string `json:"token,omitempty"`
	// ConfigRepoURL is the external git repo holding this Base's
	// ownbase.yaml. Every config mutation is committed there client-side.
	ConfigRepoURL string `json:"config_repo_url,omitempty"`
	// ConfigRef is the branch of the config repo to commit to.
	ConfigRef string `json:"config_ref,omitempty"`
	// LocalVM marks a Base backed by a local Multipass VM rather than a
	// remote server, so `delete` knows whether it may also destroy a VM of
	// the same name. A pointer because "unknown" must not be mistaken for
	// "remote".
	LocalVM *bool `json:"local_vm,omitempty"`
	// PrivateKey is the owner SSH key in OpenSSH PEM form. This is the only
	// place it exists.
	PrivateKey string `json:"private_key,omitempty"`
	// PublicKey is the matching authorized_keys line.
	PublicKey string `json:"public_key,omitempty"`
}

// KnownLocalVM reports whether the Base is definitely a local Multipass VM.
func (p Profile) KnownLocalVM() bool { return p.LocalVM != nil && *p.LocalVM }

// KnownRemote reports whether the Base is definitely a remote server. A
// profile with LocalVM unset is neither, and callers fall back to checking
// Multipass directly.
func (p Profile) KnownRemote() bool { return p.LocalVM != nil && !*p.LocalVM }

// EffectiveSSHUser returns the resolved SSH login user.
func (p Profile) EffectiveSSHUser() string {
	if p.SSHUser != "" {
		return p.SSHUser
	}
	return DefaultSSHUser
}

// EffectiveSSHPort returns the resolved SSH port.
func (p Profile) EffectiveSSHPort() int {
	if p.SSHPort > 0 {
		return p.SSHPort
	}
	return DefaultSSHPort
}

// EffectiveAPIPort returns the resolved daemon API port.
func (p Profile) EffectiveAPIPort() int {
	if p.APIPort > 0 {
		return p.APIPort
	}
	return DefaultAPIPort
}

// EffectiveConfigRef returns the resolved config-repo branch.
func (p Profile) EffectiveConfigRef() string {
	if p.ConfigRef != "" {
		return p.ConfigRef
	}
	return DefaultConfigRef
}

// Signer parses the profile's owner key into an ssh.Signer. It returns an
// error when the profile carries no key, which callers surface as "this Base
// has no owner key in the vault" rather than a parse failure.
func (p Profile) Signer() (ssh.Signer, error) {
	if strings.TrimSpace(p.PrivateKey) == "" {
		return nil, fmt.Errorf("no owner SSH key stored for this Base")
	}
	signer, err := ssh.ParsePrivateKey([]byte(p.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parse owner SSH key: %w", err)
	}
	return signer, nil
}

// PublicKeyLine returns the authorized_keys line for the profile's owner key,
// deriving it from the private half when the stored line is missing so the key
// we install can never drift from the key we authenticate with. Returns "" if
// there is no key at all.
func (p Profile) PublicKeyLine() string {
	if signer, err := p.Signer(); err == nil {
		return AuthorizedKeyLine(signer.PublicKey(), "")
	}
	return strings.TrimSpace(p.PublicKey)
}

// Redacted returns the profile with the private key removed. This is what
// leaves the credential agent: a caller gets everything it needs to address a
// Base, and asks the agent's ssh-agent socket to sign for it.
func (p Profile) Redacted() Profile {
	p.PrivateKey = ""
	return p
}

// Vault is an open (decrypted) vault. It is not safe for concurrent use;
// internal/agentd serializes access.
type Vault struct {
	store    Store
	password string
	profiles map[string]Profile

	// db is the decoded KDBX database, kept so a save does not have to pay
	// the Argon2 cost of reading the store back. Everything in it is in
	// plaintext (unlocked) form between saves.
	db *gokeepasslib.Database
	// version identifies the store contents db was decoded from (or
	// VersionNone before the first write). A save compares this against the
	// live store and re-reads when they differ, so a concurrent KeePassXC
	// edit or a second machine's write is merged rather than clobbered.
	// Three states, deliberately:
	//   - VersionNone after Create, before the first Put lands
	//   - a concrete Version after Open or a successful Save
	//   - never "unknown": a failed Head/Get is an error, not a skip-merge
	version Version
}

// saveMaxAttempts bounds the Get/merge/Put retry loop on ErrConflict.
const saveMaxAttempts = 3

// Create writes a new, empty vault to path, encrypted with password. It
// refuses to overwrite an existing file: a vault is the only copy of the keys
// that reach a Base, so clobbering one can lock the user out permanently.
func Create(path, password string) (*Vault, error) {
	return CreateStore(NewFileStore(path), password)
}

// CreateStore writes a new, empty vault into store, encrypted with password.
// Refuses to overwrite an existing object.
func CreateStore(store Store, password string) (*Vault, error) {
	if password == "" {
		return nil, errors.New("a master password is required")
	}
	if store == nil {
		return nil, errors.New("a vault store is required")
	}
	_, _, err := store.Get(context.Background())
	switch {
	case err == nil:
		return nil, fmt.Errorf("a vault already exists at %s — refusing to overwrite it", store.Location())
	case errors.Is(err, ErrNotExist):
		// ok
	default:
		return nil, err
	}
	v := &Vault{
		store:    store,
		password: password,
		profiles: map[string]Profile{},
		db:       newDatabase(password),
		version:  VersionNone,
	}
	if err := v.Save(); err != nil {
		return nil, err
	}
	return v, nil
}

// Open decrypts the vault at path with password.
func Open(path, password string) (*Vault, error) {
	return OpenStore(NewFileStore(path), password)
}

// OpenStore decrypts the vault in store with password.
func OpenStore(store Store, password string) (*Vault, error) {
	if store == nil {
		return nil, errors.New("a vault store is required")
	}
	data, ver, err := store.Get(context.Background())
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil, fmt.Errorf("no vault at %s — run: ownbasectl vault init: %w", store.Location(), err)
		}
		return nil, err
	}
	db, err := decodeBytes(data, password)
	if err != nil {
		return nil, err
	}
	v := &Vault{
		store:    store,
		password: password,
		profiles: map[string]Profile{},
		db:       db,
		version:  ver,
	}
	for _, e := range basesGroup(db).Entries {
		name := e.GetTitle()
		if name == "" {
			continue
		}
		v.profiles[name] = profileFromEntry(e)
	}
	return v, nil
}

// Path returns the store location this vault was opened from (a filesystem
// path today; a URL once remote stores land).
func (v *Vault) Path() string { return v.store.Location() }

// Store returns the persistence backend. Intended for tests and for callers
// that need to distinguish store kinds; most code should use Path().
func (v *Vault) Store() Store { return v.store }

// Names returns the configured Base names, sorted.
func (v *Vault) Names() []string {
	names := make([]string, 0, len(v.profiles))
	for n := range v.profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns the named Base's profile.
func (v *Vault) Get(name string) (Profile, error) {
	if name == "" {
		return Profile{}, errors.New("no Base name given")
	}
	p, ok := v.profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("%w: %q; run: ownbasectl list", ErrNotFound, name)
	}
	return p, nil
}

// Has reports whether the named Base has a profile.
func (v *Vault) Has(name string) bool {
	_, ok := v.profiles[name]
	return ok
}

// Put stores (or replaces) a Base's profile in memory. Call Save to persist.
func (v *Vault) Put(name string, p Profile) {
	v.profiles[name] = p
}

// Delete removes a Base's profile from memory. Call Save to persist.
func (v *Vault) Delete(name string) {
	delete(v.profiles, name)
}

// ChangePassword re-encrypts the vault under a new master password.
func (v *Vault) ChangePassword(newPassword string) error {
	if newPassword == "" {
		return errors.New("a master password is required")
	}
	old := v.password
	v.password = newPassword
	if err := v.save(old); err != nil {
		v.password = old
		return err
	}
	return nil
}

// Save re-encrypts the vault into its Store.
//
// Only the OwnBase group is replaced, so anything else the user keeps in this
// vault — their own passwords, notes, a key they added in KeePassXC — survives
// an ownbasectl write. If the store changed underneath us since we read it
// (the user edited it in another KDBX client, or a sync client pulled a newer
// copy), it is re-read first so those edits are not clobbered.
//
// The write is a compare-and-swap against the Version we last observed. On
// ErrConflict the merge is retried a bounded number of times. Atomic publish
// (temp + fsync + rename on FileStore; conditional PUT on an object store) is
// the Store's job.
func (v *Vault) Save() error { return v.save(v.password) }

// save re-encrypts the vault under v.password, reading any existing object
// with readPassword. The two differ only for ChangePassword.
func (v *Vault) save(readPassword string) error {
	ctx := context.Background()
	var lastErr error
	for attempt := 0; attempt < saveMaxAttempts; attempt++ {
		baseVersion, db, err := v.loadMergeBase(ctx, readPassword)
		if err != nil {
			return err
		}
		ciphertext, err := encodeDB(db, v.password, v.profiles)
		if err != nil {
			return err
		}
		newVersion, err := v.store.Put(ctx, ciphertext, baseVersion)
		if err == nil {
			v.db = db
			v.version = newVersion
			return nil
		}
		if !errors.Is(err, ErrConflict) {
			return err
		}
		lastErr = err
		// Conflict: loop and re-merge against the new head.
	}
	return fmt.Errorf("save vault %s: %w after %d attempts", v.store.Location(), lastErr, saveMaxAttempts)
}

// loadMergeBase returns the Version to CAS against and the KDBX database to
// write into. When the store's head differs from v.version, the live object is
// decoded and used as the merge base so foreign entries survive.
func (v *Vault) loadMergeBase(ctx context.Context, readPassword string) (Version, *gokeepasslib.Database, error) {
	data, head, err := v.store.Get(ctx)
	switch {
	case errors.Is(err, ErrNotExist):
		// Nothing in the store. Create path (baseVersion == VersionNone) or a
		// vanished object mid-session — either way there is nothing to merge.
		db := v.db
		if db == nil {
			db = newDatabase(v.password)
		}
		return VersionNone, db, nil
	case err != nil:
		// A failed Get is an error, never "skip the merge". The previous
		// fileStamp returned "" on any stat error and save treated that as
		// "nothing to merge", which would blind-overwrite on a flaky store.
		return VersionNone, nil, err
	}

	db := v.db
	if head != v.version {
		existing, err := decodeBytes(data, readPassword)
		if err != nil {
			return VersionNone, nil, err
		}
		db = existing
	}
	if db == nil {
		db = newDatabase(v.password)
	}
	return head, db, nil
}

// encodeDB builds the OwnBase group into db, reseeds, and returns the
// ciphertext. db is left unlocked (plaintext in memory) on return.
func encodeDB(db *gokeepasslib.Database, password string, profiles map[string]Profile) ([]byte, error) {
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)

	group := basesGroup(db)
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	group.Entries = make([]gokeepasslib.Entry, 0, len(names))
	for _, name := range names {
		group.Entries = append(group.Entries, entryFromProfile(name, profiles[name]))
	}

	// Fresh seeds and nonces for every write. Without this the file is
	// re-encrypted with the same ChaCha20 key and nonce each time, and two
	// versions of the vault — which cloud storage keeps by design — would
	// XOR to plaintext.
	if err := reseed(db); err != nil {
		return nil, err
	}

	if err := db.LockProtectedEntries(); err != nil {
		return nil, fmt.Errorf("protect vault entries: %w", err)
	}
	// LockProtectedEntries leaves the in-memory values encrypted, and every
	// subsequent save unlocks before locking again. Restore the plaintext
	// state this method was entered in, whatever happens below.
	defer func() { _ = db.UnlockProtectedEntries() }()

	var buf bytes.Buffer
	if err := gokeepasslib.NewEncoder(&buf).Encode(db); err != nil {
		return nil, fmt.Errorf("encode vault: %w", err)
	}
	return buf.Bytes(), nil
}

// reseed replaces every random parameter in the header that the encryption
// depends on. gokeepasslib generates these once, when the database is
// constructed, and reuses them on every encode; reusing a stream cipher's
// key and nonce across two different plaintexts is a total break, so we
// regenerate them before each write.
func reseed(db *gokeepasslib.Database) error {
	if db.Header == nil || db.Header.FileHeaders == nil {
		return errors.New("vault has no file header")
	}
	fh := db.Header.FileHeaders

	// Derived key inputs: a new master seed and a new KDF salt mean a new
	// encryption key even under the same master password.
	fh.MasterSeed = mustBytes(fh.MasterSeed, 32)
	if err := fillSlice(fh.MasterSeed); err != nil {
		return err
	}
	if fh.KdfParameters != nil {
		if _, err := rand.Read(fh.KdfParameters.Salt[:]); err != nil {
			return fmt.Errorf("read random bytes: %w", err)
		}
	}
	if len(fh.TransformSeed) > 0 { // KDBX 3.1 only
		if err := fillSlice(fh.TransformSeed); err != nil {
			return err
		}
	}

	// Cipher nonce. Keep whatever width the file's cipher uses.
	fh.EncryptionIV = mustBytes(fh.EncryptionIV, 12)
	if err := fillSlice(fh.EncryptionIV); err != nil {
		return err
	}

	// Key for the inner stream that protects individual values.
	if db.Header.IsKdbx4() {
		if db.Content != nil && db.Content.InnerHeader != nil {
			db.Content.InnerHeader.InnerRandomStreamKey = mustBytes(db.Content.InnerHeader.InnerRandomStreamKey, 64)
			if err := fillSlice(db.Content.InnerHeader.InnerRandomStreamKey); err != nil {
				return err
			}
		}
	} else {
		fh.ProtectedStreamKey = mustBytes(fh.ProtectedStreamKey, 32)
		if err := fillSlice(fh.ProtectedStreamKey); err != nil {
			return err
		}
		fh.StreamStartBytes = mustBytes(fh.StreamStartBytes, 32)
		if err := fillSlice(fh.StreamStartBytes); err != nil {
			return err
		}
	}
	return nil
}

// mustBytes returns b if it already has a length, else a new slice of n bytes.
// Existing widths are preserved because they are cipher- and format-specific.
func mustBytes(b []byte, n int) []byte {
	if len(b) > 0 {
		return b
	}
	return make([]byte, n)
}

func fillSlice(b []byte) error {
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("read random bytes: %w", err)
	}
	return nil
}

// decodeBytes decrypts a KDBX ciphertext blob.
func decodeBytes(data []byte, password string) (*gokeepasslib.Database, error) {
	db := gokeepasslib.NewDatabase()
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)
	if err := gokeepasslib.NewDecoder(bytes.NewReader(data)).Decode(db); err != nil {
		// The library cannot tell a bad password from a corrupt file — the
		// authentication tag fails either way — so neither can we.
		return nil, ErrWrongPassword
	}
	if err := db.UnlockProtectedEntries(); err != nil {
		return nil, fmt.Errorf("decrypt vault entries: %w", err)
	}
	return db, nil
}

// Argon2id cost for vaults we create. gokeepasslib's defaults (1 MiB, two
// iterations) are cheap enough to brute-force a human-typed password, and this
// file is expected to sit in cloud storage where an attacker can take their
// time with a stolen copy. 64 MiB matches the KeePassXC default; the iteration
// count puts one derivation in the few-hundred-millisecond range on current
// hardware, which is unnoticeable at unlock and expensive at scale.
//
// These live in the file header, so a vault created here keeps its cost even
// when opened by another KDBX client — and a vault created elsewhere keeps
// whatever the user chose there, because we never rewrite these on save.
const (
	argon2Memory     = 64 * 1024 * 1024
	argon2Iterations = 8
)

// newDatabase builds an empty KDBX 4 database with the OwnBase group tree.
// KDBX 4 (not 3.1) for Argon2 key derivation, which is what makes a
// human-typed master password worth relying on.
func newDatabase(password string) *gokeepasslib.Database {
	bases := gokeepasslib.NewGroup()
	bases.Name = basesGroupName

	root := gokeepasslib.NewGroup()
	root.Name = rootGroupName
	root.Groups = []gokeepasslib.Group{bases}

	db := gokeepasslib.NewDatabase(gokeepasslib.WithDatabaseKDBXVersion4())
	db.Credentials = gokeepasslib.NewPasswordCredentials(password)
	db.Content.Meta.DatabaseName = "OwnBase"
	db.Content.Root = &gokeepasslib.RootData{Groups: []gokeepasslib.Group{root}}

	if kdf := db.Header.FileHeaders.KdfParameters; kdf != nil {
		kdf.Memory = argon2Memory
		kdf.Iterations = argon2Iterations
		kdf.Parallelism = uint32(min(runtime.NumCPU(), 4))
	}
	return db
}

// basesGroup returns a pointer to the OwnBase/Bases group, creating either
// level if a vault made elsewhere (or by an older version) lacks it.
func basesGroup(db *gokeepasslib.Database) *gokeepasslib.Group {
	if db.Content.Root == nil {
		db.Content.Root = &gokeepasslib.RootData{}
	}
	root := findOrAddGroup(&db.Content.Root.Groups, rootGroupName)
	return findOrAddGroup(&root.Groups, basesGroupName)
}

func findOrAddGroup(groups *[]gokeepasslib.Group, name string) *gokeepasslib.Group {
	for i := range *groups {
		if (*groups)[i].Name == name {
			return &(*groups)[i]
		}
	}
	g := gokeepasslib.NewGroup()
	g.Name = name
	*groups = append(*groups, g)
	return &(*groups)[len(*groups)-1]
}

func entryFromProfile(name string, p Profile) gokeepasslib.Entry {
	e := gokeepasslib.NewEntry()
	set := func(key, value string) {
		if value != "" {
			e.Values = append(e.Values, value_(key, value, false))
		}
	}
	setProtected := func(key, value string) {
		if value != "" {
			e.Values = append(e.Values, value_(key, value, true))
		}
	}

	set(fieldTitle, name)
	set(fieldURL, p.Host)
	set(fieldUserName, p.SSHUser)
	setProtected(fieldPassword, p.Token)
	setProtected(fieldPrivateKey, p.PrivateKey)
	set(fieldPublicKey, p.PublicKey)
	if p.SSHPort > 0 {
		set(fieldSSHPort, strconv.Itoa(p.SSHPort))
	}
	if p.APIPort > 0 {
		set(fieldAPIPort, strconv.Itoa(p.APIPort))
	}
	set(fieldConfigRepoURL, p.ConfigRepoURL)
	set(fieldConfigRef, p.ConfigRef)
	if p.LocalVM != nil {
		set(fieldLocalVM, strconv.FormatBool(*p.LocalVM))
	}
	set(fieldNotes, entryNotes)
	return e
}

func value_(key, content string, protected bool) gokeepasslib.ValueData {
	return gokeepasslib.ValueData{
		Key:   key,
		Value: gokeepasslib.V{Content: content, Protected: w.NewBoolWrapper(protected)},
	}
}

func profileFromEntry(e gokeepasslib.Entry) Profile {
	p := Profile{
		Host:          e.GetContent(fieldURL),
		SSHUser:       e.GetContent(fieldUserName),
		Token:         e.GetPassword(),
		ConfigRepoURL: e.GetContent(fieldConfigRepoURL),
		ConfigRef:     e.GetContent(fieldConfigRef),
		PrivateKey:    e.GetContent(fieldPrivateKey),
		PublicKey:     e.GetContent(fieldPublicKey),
	}
	if n, err := strconv.Atoi(e.GetContent(fieldSSHPort)); err == nil {
		p.SSHPort = n
	}
	if n, err := strconv.Atoi(e.GetContent(fieldAPIPort)); err == nil {
		p.APIPort = n
	}
	if raw := e.GetContent(fieldLocalVM); raw != "" {
		if b, err := strconv.ParseBool(raw); err == nil {
			p.LocalVM = &b
		}
	}
	return p
}
