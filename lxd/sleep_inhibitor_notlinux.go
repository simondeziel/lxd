//go:build !linux

package main

import (
	"github.com/canonical/lxd/lxd/state"
)

// setupSleepInhibitor is a no-op on non-Linux platforms because 
// systemd-logind and D-Bus sleep inhibitors are Linux-specific.
func setupSleepInhibitor(s *state.State) {
    // Do nothing
}

