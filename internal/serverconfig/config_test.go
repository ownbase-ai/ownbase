package serverconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ownbase/ownbase/internal/serverconfig"
)

func TestServerProfile_LocalVMTriState(t *testing.T) {
	trueVal, falseVal := true, false
	cases := []struct {
		name            string
		localVM         *bool
		wantKnownLocal  bool
		wantKnownRemote bool
	}{
		{"unset (legacy profile)", nil, false, false},
		{"explicitly local", &trueVal, true, false},
		{"explicitly remote", &falseVal, false, true},
	}
	for _, c := range cases {
		p := serverconfig.ServerProfile{LocalVM: c.localVM}
		if got := p.KnownLocalVM(); got != c.wantKnownLocal {
			t.Errorf("%s: KnownLocalVM() = %v, want %v", c.name, got, c.wantKnownLocal)
		}
		if got := p.KnownRemote(); got != c.wantKnownRemote {
			t.Errorf("%s: KnownRemote() = %v, want %v", c.name, got, c.wantKnownRemote)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := serverconfig.Load("/nonexistent/path/config")
	if err != nil {
		t.Fatalf("Load of missing file returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("expected empty servers map, got %d entries", len(cfg.Servers))
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")

	cfg := &serverconfig.Config{
		Servers: map[string]serverconfig.ServerProfile{
			"mybase": {
				Host:    "192.168.1.100",
				SSHUser: "ubuntu",
				SSHKey:  "~/.ssh/id_ed25519",
				APIPort: 7070,
				Token:   "abc123",
			},
		},
	}

	if err := serverconfig.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := serverconfig.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := loaded.Servers["mybase"]
	if !ok {
		t.Fatal("server 'mybase' not found after load")
	}
	if p.Host != "192.168.1.100" {
		t.Errorf("Host: got %q, want %q", p.Host, "192.168.1.100")
	}
	if p.Token != "abc123" {
		t.Errorf("Token: got %q, want %q", p.Token, "abc123")
	}

	// Config file must be mode 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode: got %04o, want 0600", info.Mode().Perm())
	}
}

func TestSaveCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config")
	cfg := &serverconfig.Config{Servers: make(map[string]serverconfig.ServerProfile)}
	if err := serverconfig.Save(path, cfg); err != nil {
		t.Fatalf("Save with missing parent dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
}

func TestProfileFor(t *testing.T) {
	cfg := &serverconfig.Config{
		Servers: map[string]serverconfig.ServerProfile{
			"prod": {Host: "prod.example.com", Token: "tok1"},
			"dev":  {Host: "dev.example.com", Token: "tok2"},
		},
	}

	// Named lookup.
	p, err := cfg.ProfileFor("dev")
	if err != nil {
		t.Fatalf("ProfileFor(dev): %v", err)
	}
	if p.Host != "dev.example.com" {
		t.Errorf("Host: got %q, want dev.example.com", p.Host)
	}

	// Missing server.
	_, err = cfg.ProfileFor("staging")
	if err == nil {
		t.Error("expected error for missing server, got nil")
	}

	// Empty name — there is no default Base to fall back to.
	_, err = cfg.ProfileFor("")
	if err == nil {
		t.Error("expected error for empty name, got nil")
	}
}

func TestEffectiveDefaults(t *testing.T) {
	p := serverconfig.ServerProfile{}
	if p.EffectiveSSHUser() != serverconfig.DefaultSSHUser {
		t.Errorf("EffectiveSSHUser: got %q, want %q", p.EffectiveSSHUser(), serverconfig.DefaultSSHUser)
	}
	if p.EffectiveAPIPort() != serverconfig.DefaultAPIPort {
		t.Errorf("EffectiveAPIPort: got %d, want %d", p.EffectiveAPIPort(), serverconfig.DefaultAPIPort)
	}
}
