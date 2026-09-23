// Package sysupdate backs `slfhst update check|apply`, replacing bootc's
// role entirely. Talks to systemd-sysupdated over D-Bus
// (org.freedesktop.sysupdate1) via internal/dbus -- confirmed by
// inspecting the real running daemon (unit file has
// BusName=org.freedesktop.sysupdate1, no Varlink socket or strings in
// the binary at all) that this is D-Bus-only, correcting an earlier
// draft of the plan that assumed a Varlink interface existed.
//
// The core mechanics of internal/dbus itself (message framing,
// marshaling, simple + array + struct signatures) were verified against
// this exact daemon's Manager.ListTargets ("a(sso)") in a real container;
// see the plan's Phase 0 notes. What's NOT yet verified: an actual
// CheckNew/Update exercise against a real configured target (no transfer
// definitions exist in the verification environment), including exactly
// what error CheckNew returns when already up to date -- flagged inline
// below rather than guessed at.
package sysupdate

import (
	"fmt"

	"github.com/nnnithinnn/slfhst/internal/dbus"
)

const (
	busName      = "org.freedesktop.sysupdate1"
	managerPath  = "/org/freedesktop/sysupdate1"
	managerIface = "org.freedesktop.sysupdate1.Manager"
	targetIface  = "org.freedesktop.sysupdate1.Target"
)

type target struct {
	class, name, path string
}

func listTargets(c *dbus.Conn) ([]target, error) {
	var raw []any
	if err := c.Call(busName, managerPath, managerIface, "ListTargets", "", nil, "a(sso)", []any{&raw}); err != nil {
		return nil, fmt.Errorf("sysupdate: ListTargets: %w", err)
	}
	targets := make([]target, 0, len(raw))
	for _, item := range raw {
		fields, ok := item.([]any)
		if !ok || len(fields) != 3 {
			continue
		}
		class, _ := fields[0].(string)
		name, _ := fields[1].(string)
		path, _ := fields[2].(string)
		targets = append(targets, target{class: class, name: name, path: path})
	}
	return targets, nil
}

// defaultTarget picks the appliance's own OS-image target -- matches
// what the systemd-sysupdate CLI does implicitly when invoked with no
// -C/--component (per its --help: `-C --component=NAME Select component
// to update`, and systemd-sysupdate.d(5) documents "host" as the class
// for the base OS target). Falls back to the only target if there's
// exactly one, so a single-target appliance (this one) works even if the
// class naming ever turns out to differ from "host" in practice --
// that assumption isn't yet confirmed against a real configured target.
func defaultTarget(targets []target) (target, error) {
	for _, t := range targets {
		if t.class == "host" {
			return t, nil
		}
	}
	if len(targets) == 1 {
		return targets[0], nil
	}
	return target{}, fmt.Errorf("sysupdate: no default (host) target found among %d target(s)", len(targets))
}

// CheckNewVersion opens its own bus connection and returns the new
// version string reported by the appliance's default target's
// CheckNew() -- the low-level primitive both `slfhst update check` (via
// Check(), below) and internal/monitor's periodic status line use, so
// neither duplicates the bus connection / target lookup logic.
func CheckNewVersion() (string, error) {
	c, err := dbus.SystemBus()
	if err != nil {
		return "", fmt.Errorf("sysupdate: %w", err)
	}
	defer c.Close()

	t, err := currentTarget(c)
	if err != nil {
		return "", err
	}

	var newVersion string
	err = c.Call(busName, t.path, targetIface, "CheckNew", "", nil, "s", []any{&newVersion})
	if err != nil {
		// TODO: confirm the actual error name systemd-sysupdated
		// returns when already up to date (no transfer definitions
		// configured in any environment this was tested against, so
		// this path is unverified) -- for now, surface it rather than
		// silently treating every error as "no update available".
		return "", fmt.Errorf("sysupdate: CheckNew: %w", err)
	}
	return newVersion, nil
}

// Check reports whether a newer version is available.
func Check() error {
	newVersion, err := CheckNewVersion()
	if err != nil {
		return err
	}
	fmt.Println("new version available:", newVersion)
	return nil
}

// Apply triggers the atomic A/B usr+UKI swap to the newest available
// version.
func Apply() error {
	c, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("sysupdate: %w", err)
	}
	defer c.Close()

	t, err := currentTarget(c)
	if err != nil {
		return err
	}

	var newVersion string
	if err := c.Call(busName, t.path, targetIface, "CheckNew", "", nil, "s", []any{&newVersion}); err != nil {
		return fmt.Errorf("sysupdate: CheckNew: %w", err)
	}

	var appliedVersion string
	var jobID uint64
	var jobPath string
	err = c.Call(busName, t.path, targetIface, "Update", "st", []any{newVersion, uint64(0)}, "sto",
		[]any{&appliedVersion, &jobID, &jobPath})
	if err != nil {
		return fmt.Errorf("sysupdate: Update: %w", err)
	}
	fmt.Printf("updating to %s (job %d, %s)\n", appliedVersion, jobID, jobPath)
	return nil
}

func currentTarget(c *dbus.Conn) (target, error) {
	targets, err := listTargets(c)
	if err != nil {
		return target{}, err
	}
	return defaultTarget(targets)
}
