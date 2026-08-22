package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveLatestDaemon(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"schema": 1,
			"components": {
				"cli": {"version": "v0.5.5"},
				"app": {"version": "v0.5.5"},
				"daemon": {"version": "v0.5.5"}
			}
		}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tag, err := resolveLatestDaemon(context.Background(), srv.URL+"/daemon")
	if err != nil {
		t.Fatalf("resolveLatestDaemon: %v", err)
	}
	if tag != "v0.5.5" {
		t.Fatalf("tag = %q, want v0.5.5", tag)
	}
}

func TestResolveLatestDaemon_missing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"components":{"cli":{"version":"v1.0.0"}}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := resolveLatestDaemon(context.Background(), srv.URL+"/daemon")
	if err == nil {
		t.Fatal("expected error when daemon version missing")
	}
}

func TestApply_latestResolvesThenNoopsWhenCurrent(t *testing.T) {
	// When latest.json says the same tag as CurrentVersion, Apply must not
	// download a binary at all.
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"components":{"daemon":{"version":"v0.5.1"}}}`))
	})
	mux.HandleFunc("/daemon/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected binary fetch: %s", r.URL.Path)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var log strings.Builder
	res, err := Apply(context.Background(), Options{
		Version:        "latest",
		ReleaseBaseURL: srv.URL + "/daemon",
		CurrentVersion: "v0.5.1",
		SkipVerify:     true,
		Writer:         &log,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Updated {
		t.Fatalf("expected no-op update, got %+v", res)
	}
	if res.To != "v0.5.1" {
		t.Fatalf("To = %q", res.To)
	}
	if !strings.Contains(log.String(), "Latest release is v0.5.1") {
		t.Fatalf("log missing resolve line:\n%s", log.String())
	}
}

func TestApply_latestDownloadsVersionedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod + exec version probe")
	}
	arch, err := Arch()
	if err != nil {
		t.Skip(err)
	}

	// A tiny script that answers --version like ownbased does.
	// We install it to a temp BinaryPath so Apply's rename target is writable.
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "ownbased")

	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":1,"components":{"daemon":{"version":"v9.9.9"}}}`))
	})
	binPath := "/daemon/v9.9.9/ownbased-linux-" + arch
	mux.HandleFunc(binPath, func(w http.ResponseWriter, r *http.Request) {
		// shell script posing as the daemon binary
		_, _ = w.Write([]byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'ownbased v9.9.9'; exit 0; fi\nexit 0\n"))
	})
	mux.HandleFunc(binPath+".minisig", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // unused — SkipVerify
	})
	// Stale latest/ path must not be touched.
	mux.HandleFunc("/daemon/latest/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not fetch mutable latest/ path: %s", r.URL.Path)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var log strings.Builder
	res, err := Apply(context.Background(), Options{
		Version:        "latest",
		ReleaseBaseURL: srv.URL + "/daemon",
		BinaryPath:     dest,
		CurrentVersion: "v0.5.1",
		SkipVerify:     true,
		Writer:         &log,
	})
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log.String())
	}
	if !res.Updated || !res.RestartPending {
		t.Fatalf("expected update+restart, got %+v\nlog:\n%s", res, log.String())
	}
	if res.To != "v9.9.9" {
		t.Fatalf("To = %q, want v9.9.9", res.To)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("dest not installed: %v", err)
	}
	if !strings.Contains(log.String(), "/daemon/v9.9.9/ownbased-linux-") {
		t.Fatalf("expected versioned download URL in log:\n%s", log.String())
	}
}
