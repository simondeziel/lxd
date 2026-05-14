//go:build linux

package main

import (
	"context"
	"syscall"
	"time"

	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/shared/logger"
	"github.com/godbus/dbus/v5"
)

// setupSleepInhibitor connects to systemd-logind via D-Bus to freeze running
// containers before the host suspends, and unfreezes them on resume.
func setupSleepInhibitor(s *state.State) {
	conn, err := dbus.SystemBus()
	if err != nil {
		logger.Warnf("Failed connecting to system D-Bus for sleep inhibitor: %v", err)
		return
	}

	// Subscribe to the PrepareForSleep signal from logind.
	err = conn.AddMatchSignal(
		dbus.WithMatchObjectPath("/org/freedesktop/login1"),
		dbus.WithMatchInterface("org.freedesktop.login1.Manager"),
		dbus.WithMatchMember("PrepareForSleep"),
	)
	if err != nil {
		logger.Warnf("Failed adding D-Bus match for PrepareForSleep: %v", err)
		_ = conn.Close()
		return
	}

	c := make(chan *dbus.Signal, 10)
	conn.Signal(c)

	go func() {
		defer conn.Close()

		// lockFD holds the systemd-logind delay inhibitor file descriptor.
		// -1 means no lock is currently held.
		lockFD := dbus.UnixFD(-1)
		var frozenInstances []instance.Instance

		// grabLock acquires a delay inhibitor lock from systemd-logind that
		// prevents the host from sleeping until the lock is released.
		grabLock := func() {
			lockFD = -1
			obj := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
			call := obj.Call("org.freedesktop.login1.Manager.Inhibit", 0,
				"sleep",                                 // what
				"LXD",                                   // who
				"Pause running containers before sleep", // why
				"delay",                                 // mode
			)
			if call.Err != nil {
				logger.Warnf("Failed getting systemd sleep inhibitor lock: %v", call.Err)
				return
			}

			call.Store(&lockFD)
		}

		// Grab the initial lock on startup.
		grabLock()

		for {
			select {
			case <-s.ShutdownCtx.Done():
				// Release the inhibitor lock before exiting so a pending
				// suspend is not blocked indefinitely.
				if lockFD >= 0 {
					_ = syscall.Close(int(lockFD))
				}

				return

			case v, ok := <-c:
				if !ok {
					return
				}

				if v.Name != "org.freedesktop.login1.Manager.PrepareForSleep" || len(v.Body) != 1 {
					continue
				}

				sleeping, ok := v.Body[0].(bool)
				if !ok {
					continue
				}

				if sleeping {
					logger.Info("System is going to sleep, freezing running containers")

					// Only load containers; VMs do not use lxcfs.
					instances, err := instance.LoadNodeAll(s, instancetype.Container)
					if err != nil {
						logger.Errorf("Failed loading containers for sleep inhibitor: %v", err)
					} else {
						frozenInstances = nil
						for _, inst := range instances {
							if !inst.IsRunning() {
								continue
							}

							freezeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
							err := inst.Freeze(freezeCtx)
							cancel()
							if err != nil {
								logger.Warnf("Failed freezing container %q: %v", inst.Name(), err)
							} else {
								// Track containers we froze so we can unfreeze them on resume.
								frozenInstances = append(frozenInstances, inst)
							}
						}
					}

					// Release the lock to allow the system to sleep, regardless
					// of whether freezing succeeded.
					if lockFD >= 0 {
						_ = syscall.Close(int(lockFD))
						lockFD = -1
					}
				} else {
					logger.Info("System woke up, unfreezing containers")

					for _, inst := range frozenInstances {
						unfreezeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						err := inst.Unfreeze(unfreezeCtx)
						cancel()
						if err != nil {
							logger.Warnf("Failed unfreezing container %q: %v", inst.Name(), err)
						}
					}

					frozenInstances = nil

					// Grab a new lock for the next sleep cycle.
					grabLock()
				}
			}
		}
	}()
}
