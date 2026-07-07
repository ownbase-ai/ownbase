package devbridge_test

import (
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"testing"

	"github.com/ownbase/ownbase/internal/devbridge"
)

func TestMkcertAvailable_DoesNotPanic(t *testing.T) {
	// Just exercise the PATH lookup — true/false both fine depending on the
	// machine running the test.
	_ = devbridge.MkcertAvailable()
}

func TestGenerateCert_NoHostnamesErrors(t *testing.T) {
	if _, _, err := devbridge.GenerateCert(nil, t.TempDir()); err == nil {
		t.Error("expected error when no hostnames are given")
	}
}

// TestGenerateCert_Integration exercises the real mkcert binary when it is
// available on PATH (e.g. developer machines, `brew install mkcert`); it
// skips gracefully in CI environments without mkcert installed.
func TestGenerateCert_Integration(t *testing.T) {
	if !devbridge.MkcertAvailable() {
		t.Skip("mkcert not installed — skipping integration test")
	}
	if err := devbridge.MkcertEnsureInstalled(); err != nil {
		t.Skipf("mkcert -install failed (likely needs sudo in this environment): %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "certs")
	hostnames := []string{"app.example.com.localhost", "app.example.org.localhost"}
	certPath, keyPath, err := devbridge.GenerateCert(hostnames, destDir)
	if err != nil {
		t.Fatalf("GenerateCert: %v", err)
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load generated cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	for _, host := range hostnames {
		if err := leaf.VerifyHostname(host); err != nil {
			t.Errorf("cert does not cover hostname %q: %v", host, err)
		}
	}
}
