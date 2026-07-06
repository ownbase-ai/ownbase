package main

// dev.go implements `ownbasectl dev <name>` — the one human-run, explicitly
// interactive command in ownbasectl (see docs/decisions.md for why this
// split exists). Starting this long-running command is itself the human's
// "I am sitting here, ready to develop" signal, so it is the only place a
// one-time `sudo mkcert -install` prompt is acceptable. `create`/`vm` never
// prompt for anything — see cmd/ownbasectl/create.go.
//
// What it does, step by step:
//  1. Reads the target's ownbase.yaml over SSH and finds every service with
//     both a port and at least one domain configured
//     (internal/devbridge.Discover). A service with no domain is never
//     bridged, mirroring exactly what is already intentionally publicly
//     reachable in production.
//  2. Ensures mkcert's local CA is trusted (one-time sudo prompt, ever).
//  3. Opens one SSH tunnel per bridged service (internal/tunnel, reused
//     completely unmodified) directly to its container port, bypassing
//     Caddy entirely.
//  4. Generates one mkcert certificate covering every bridged hostname:
//     each service's real domain with ".localhost" appended verbatim,
//     e.g. "myapp.example.com" -> "myapp.example.com.localhost" (RFC 6761 —
//     always resolves to loopback, no DNS, no /etc/hosts, works offline).
//  5. Serves a local HTTPS reverse proxy dispatching by Host header.
//  6. Blocks until Ctrl+C (SIGINT/SIGTERM), then closes every tunnel.
//
// There is no code-sync mechanism here of any kind: this command only
// tunnels and proxies traffic to whatever is currently deployed — no bind
// mount, file watcher, or hot-reload path. To iterate on a service's code,
// push to its bare repo and update ref: exactly as in production (see
// docs/development.md) — the dev bridge, if still running, picks up the
// new container transparently since it tunnels to the service's port, not
// to a specific container instance.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/devbridge"
	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/tunnel"
)

// DefaultDevBridgePort is the local HTTPS port `ownbasectl dev` binds to.
// Deliberately not 443: binding a privileged port would add a second,
// independent permission requirement on top of the one-time mkcert install.
const DefaultDevBridgePort = 8443

// devBridgeShutdownTimeout bounds how long graceful HTTP server shutdown
// waits for in-flight requests to finish after Ctrl+C.
const devBridgeShutdownTimeout = 5 * time.Second

func newDevCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "dev <name>",
		Short: "Local HTTPS dev bridge: reach a Base's domain'd services over SSH tunnels",
		Long: `ownbasectl dev is the one place in ownbasectl a human deliberately opts
into an interactive session — starting it is itself the "I am sitting here,
ready to develop" signal, so it is the only command allowed to prompt for
sudo (mkcert's one-time local CA install). create/vm never prompt for
anything.

It reads the Base's live ownbase.yaml over SSH, opens one SSH tunnel per
service that has both a port and a domain: (or domains:) configured — a
service with no domain is never bridged — and serves each at
https://<domain>.localhost:8443: its real, already-configured production
domain with ".localhost" appended, which every OS/browser resolves straight
to loopback (RFC 6761) with no /etc/hosts entry, no DNS lookup, and no
dependency on the Base's IP address (surviving VM stop/start unchanged).

There is no code-sync mechanism: to iterate on a service's code, push a
branch to its bare repo and run 'ownbasectl service update --ref', exactly
as in production (see docs/development.md). The dev bridge, if still
running, picks up the new container transparently.`,
		Example: `  ownbasectl dev mybase
  ownbasectl dev mybase --port 9443`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(args[0], port)
		},
	}
	cmd.Flags().IntVar(&port, "port", DefaultDevBridgePort, "local port to serve HTTPS on")
	return cmd
}

func runDev(name string, port int) error {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	profile, err := cfg.ProfileFor(name)
	if err != nil {
		return err
	}
	if profile.Host == "" {
		return fmt.Errorf("Base %q has no host recorded", name)
	}

	if !devbridge.MkcertAvailable() {
		return fmt.Errorf("%s", devbridge.MkcertInstallHint)
	}
	fmt.Fprintln(os.Stderr, "ownbasectl: ensuring mkcert's local CA is trusted (sudo may prompt once, ever) ...")
	if err := devbridge.MkcertEnsureInstalled(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "ownbasectl: reading ownbase.yaml from %q ...\n", name)
	raw, err := tunnel.RunCommand(
		profile.Host, profile.EffectiveSSHUser(), profile.EffectiveSSHKey(),
		"cat /opt/ownbase/checkout/ownbase.yaml", profile.EffectiveSSHPort(),
	)
	if err != nil {
		return fmt.Errorf("read ownbase.yaml from %q over SSH: %w", name, err)
	}

	targets, err := devbridge.Discover(raw)
	if err != nil {
		return fmt.Errorf("parse ownbase.yaml from %q: %w", name, err)
	}
	if len(targets) == 0 {
		fmt.Printf("No service on %q has a domain: (or domains:) configured yet — nothing to bridge.\n", name)
		fmt.Println("Add one (e.g. ownbasectl service update <base> <service> --domain <domain>) and re-run.")
		return nil
	}

	fmt.Fprintf(os.Stderr, "ownbasectl: opening %d SSH tunnel(s) to %q ...\n", len(targets), name)
	var tunnels []*tunnel.Tunnel
	defer func() {
		for _, t := range tunnels {
			_ = t.Close()
		}
	}()

	routes := make(map[string]string) // local hostname -> tunnel local addr
	var hostnames []string
	for _, target := range targets {
		tun, err := tunnel.Open(
			profile.Host, profile.EffectiveSSHUser(), profile.EffectiveSSHKey(),
			target.Port, profile.EffectiveSSHPort(),
		)
		if err != nil {
			return fmt.Errorf("open SSH tunnel for service %q (port %d): %w", target.Service, target.Port, err)
		}
		tunnels = append(tunnels, tun)
		for _, h := range target.LocalHostnames() {
			routes[h] = tun.LocalAddr()
			hostnames = append(hostnames, h)
		}
	}
	sort.Strings(hostnames)

	certDir := filepath.Join(filepath.Dir(cfgPath), "dev-bridge", name)
	fmt.Fprintf(os.Stderr, "ownbasectl: generating local HTTPS certificate for %d hostname(s) ...\n", len(hostnames))
	certPath, keyPath, err := devbridge.GenerateCert(hostnames, certDir)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("load generated certificate: %w", err)
	}

	handler, err := devbridge.NewProxyHandler(routes)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:      fmt.Sprintf("127.0.0.1:%d", port),
		Handler:   handler,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}

	fmt.Println()
	fmt.Println("Bridging:")
	for _, h := range hostnames {
		fmt.Printf("  https://%s:%d\n", h, port)
	}
	fmt.Println()
	fmt.Println("No code-sync — push to the service's bare repo and update ref: to deploy changes.")
	fmt.Println("Press Ctrl+C to stop.")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServeTLS("", "")
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "\nownbasectl: shutting down dev bridge ...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), devBridgeShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}
}
