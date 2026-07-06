package install

import (
	"os"
	"strings"
)

// FirstRunEnvPath is the one-time configuration file written by install.sh
// and consumed by the agent on its first bootstrapCore call. The file is
// root-only (0600) and deleted after successful consumption.
const FirstRunEnvPath = "/opt/ownbase/first-run.env"

// FirstRunEnv holds the values parsed from the one-time first-run.env file
// written by install.sh. All fields are optional — the installer only writes
// values that were explicitly provided.
type FirstRunEnv struct {
	// CaddyEmail is the ACME/Let's Encrypt contact email for automatic TLS.
	// Empty when not provided at install time.
	CaddyEmail string
}

// ReadFirstRunEnv parses the first-run configuration from the file at path.
// Returns a zero-value FirstRunEnv when the file does not exist or cannot be
// read. The file is NOT deleted — call DeleteFirstRunEnv after a successful
// bootstrap.
func ReadFirstRunEnv(path string) FirstRunEnv {
	var env FirstRunEnv
	data, err := os.ReadFile(path)
	if err != nil {
		return env
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "CADDY_EMAIL":
			env.CaddyEmail = v
		}
	}
	return env
}

// DeleteFirstRunEnv removes the one-time credentials file. Errors are
// silently ignored — the file may already be absent.
func DeleteFirstRunEnv(path string) {
	_ = os.Remove(path)
}
