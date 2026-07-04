// Package ownbase holds repo-level embedded assets shared by the OwnBase
// binaries. The installer script lives at the repo root (where operators and
// docs expect to find it) and is embedded here so a released ownbasectl can
// install a Base without a source checkout.
package ownbase

import _ "embed"

// InstallScript is the OwnBase installer (install.sh), embedded at build
// time. `ownbasectl create` uploads and runs it on the target machine —
// locally on a Multipass VM or over SSH on a remote server.
//
//go:embed install.sh
var InstallScript []byte
