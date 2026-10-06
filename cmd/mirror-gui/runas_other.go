//go:build !linux

package main

import "github.com/nr3v0/mirror-gui/internal/server"

// dropPrivileges is only needed in the Linux container image.
func dropPrivileges(server.Config) error { return nil }
