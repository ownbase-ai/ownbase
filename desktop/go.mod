// Not a Go module anyone builds — a fence.
//
// There is no first-party Go code under desktop/. This file exists because
// `go test ./...` from the repo root walks into desktop/node_modules, where a
// transitive npm dependency (flatted) ships a stray Go package. The go command
// does not skip node_modules, but it does skip any directory containing its own
// go.mod, so this one line keeps the repo's test and lint output limited to code
// we actually wrote.
//
// The desktop app is built with npm and cargo; see desktop/README.md.
module github.com/ownbase/ownbase/desktop

go 1.24.3
