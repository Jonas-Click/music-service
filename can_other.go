//go:build !linux

package main

import "fmt"

// SocketCAN is Linux-only. This stub lets the package build (and `go vet`,
// `go test ./...`) run on a laptop — use --device log: or --device null: for
// local development; only a real deploy target needs openSocketCAN to work.
func openSocketCAN(iface string) (canSink, error) {
	return nil, fmt.Errorf("socketcan is not available on this platform — use --device log: or null: for local development")
}
