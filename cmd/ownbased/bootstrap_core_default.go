//go:build !integration

package main

import (
	"context"

	"github.com/ownbase/ownbase/internal/schema"
)

// bootstrapCore is a no-op without the integration build tag.
// Core packages (Forgejo, Caddy) are managed manually in dev/CI.
func bootstrapCore(_ context.Context, _ agentConfig, _ schema.CoreConfig) error {
	return nil
}

// discoverForgejoURL returns the default URL unchanged; no container to inspect.
func discoverForgejoURL(defaultURL string) string { return defaultURL }
