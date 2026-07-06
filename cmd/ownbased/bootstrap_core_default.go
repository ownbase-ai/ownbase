//go:build !integration

package main

import (
	"context"

	"github.com/ownbase/ownbase/internal/schema"
)

// bootstrapCore is a no-op without the integration build tag.
// The core package (Caddy) is managed manually in dev/CI.
func bootstrapCore(_ context.Context, _ agentConfig, _ schema.CoreConfig) error {
	return nil
}
