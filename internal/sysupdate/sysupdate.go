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
// see the plan's Phase 0 notes.
//
// Two systemd-sysupdate targets are managed here, matching the two
// .transfer definitions shipped in mkosi.extra
// (usr/lib/sysupdate.d/50-usr.transfer and
// usr/lib/sysupdate.uki.d/70-uki.transfer): the "host" class target
// (the usr A/B partition swap) and the "uki" named component (the UKI
// on the ESP, tracked separately so it gets its own boot-counting
// lifecycle). Both class/name pairings confirmed via DeepWiki against
// sysupdate-target.c's target_class_table. What's NOT yet verified: an
// actual CheckNew/Update exercise against a real GitHub-hosted
// SHA256SUMS manifest, including exactly what error CheckNew returns
// when already up to date -- flagged inline below rather than guessed
// at.
package sysupdate

import (
	"errors"
	"fmt"

	"github.com/nnnithinnn/slfhst/internal/dbus"
)

const (
	busName      = "org.freedesktop.sysupdate1"
	managerPath  = "/org/freedesktop/sysupdate1"
	managerIface = "org.freedesktop.sysupdate1.Manager"
	targetIface  = "org.freedesktop.sysupdate1.Target"

	// componentUKI is the component name systemd-sysupdate reports for
	// the transfer defined by mkosi.extra's
	// usr/lib/sysupdate.uki.d/70-uki.transfer (the directory name after
	// "sysupdate." is the component name).
	componentUKI = "uki"
)

type target struct {
	class, name, path string
}

func (t target) isHost() bool { return t.class == "host" }
func (t target) isUKI() bool  { return t.class == "component" && t.name == componentUKI }

// label identifies a target in CLI output.
func (t target) label() string {
	if t.name != "" {
		return t.name
	}
	return "host"
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

// managedTargets returns, in a stable order (host first, then uki), the
// targets this appliance actually manages. Falls back to the raw target
// list if neither matched by class/name -- covers an environment
// missing the uki.d transfer (e.g. mid-rollout) or the class naming
// ever turning out to differ from what DeepWiki's source citations
// confirmed, so a still-useful target isn't hidden behind a strict
// filter.
func managedTargets(all []target) ([]target, error) {
	var out []target
	for _, t := range all {
		if t.isHost() {
			out = append(out, t)
		}
	}
	for _, t := range all {
		if t.isUKI() {
			out = append(out, t)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if len(all) > 0 {
		return all, nil
	}
	return nil, fmt.Errorf("sysupdate: no targets found (expected the host usr target and/or the %q component)", componentUKI)
}

func currentTargets(c *dbus.Conn) ([]target, error) {
	targets, err := listTargets(c)
	if err != nil {
		return nil, err
	}
	return managedTargets(targets)
}

func checkNew(c *dbus.Conn, t target) (string, error) {
	var newVersion string
	err := c.Call(busName, t.path, targetIface, "CheckNew", "", nil, "s", []any{&newVersion})
	if err != nil {
		// TODO: confirm the actual error name systemd-sysupdated
		// returns when already up to date -- surfaced rather than
		// silently treating every error as "no update available".
		return "", fmt.Errorf("CheckNew: %w", err)
	}
	return newVersion, nil
}

// CheckNewVersion opens its own bus connection and returns the new
// version string reported by the appliance's host (usr) target's
// CheckNew() -- the low-level primitive internal/monitor's periodic
// status line uses. Only the host target is checked here, not the uki
// component: both are published from the exact same versioned release
// in lockstep (mkosi/build-all.sh embeds matching
// slfhst_<version>.usr.raw and .efi in one publish), so the host
// target's version is a faithful proxy for whether any update is
// available, without monitor's single-line status needing to reason
// about two targets separately. `slfhst update check` (Check(), below)
// reports on both targets individually.
func CheckNewVersion() (string, error) {
	c, err := dbus.SystemBus()
	if err != nil {
		return "", fmt.Errorf("sysupdate: %w", err)
	}
	defer c.Close()

	managed, err := currentTargets(c)
	if err != nil {
		return "", fmt.Errorf("sysupdate: %w", err)
	}

	t := managed[0]
	for _, cand := range managed {
		if cand.isHost() {
			t = cand
			break
		}
	}
	v, err := checkNew(c, t)
	if err != nil {
		return "", fmt.Errorf("sysupdate: %s: %w", t.label(), err)
	}
	return v, nil
}

// Check reports, for each target this appliance manages (host usr
// partition and the uki component), whether a newer version is
// available.
func Check() error {
	c, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("sysupdate: %w", err)
	}
	defer c.Close()

	managed, err := currentTargets(c)
	if err != nil {
		return fmt.Errorf("sysupdate: %w", err)
	}

	var errs []error
	for _, t := range managed {
		v, err := checkNew(c, t)
		if err != nil {
			fmt.Printf("%s: check failed: %v\n", t.label(), err)
			errs = append(errs, fmt.Errorf("%s: %w", t.label(), err))
			continue
		}
		fmt.Printf("%s: new version available: %s\n", t.label(), v)
	}
	return errors.Join(errs...)
}

// Apply triggers the atomic update to the newest available version for
// every target this appliance manages -- the usr A/B partition swap and
// the uki component's ESP deployment. Each target is attempted
// independently (a failure on one doesn't block the other) since
// systemd-sysupdate has no cross-component transaction; both fetch from
// the same versioned release, so under normal operation they either
// both have a new version or neither does.
func Apply() error {
	c, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("sysupdate: %w", err)
	}
	defer c.Close()

	managed, err := currentTargets(c)
	if err != nil {
		return fmt.Errorf("sysupdate: %w", err)
	}

	var errs []error
	for _, t := range managed {
		newVersion, err := checkNew(c, t)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.label(), err))
			continue
		}

		var appliedVersion string
		var jobID uint64
		var jobPath string
		err = c.Call(busName, t.path, targetIface, "Update", "st", []any{newVersion, uint64(0)}, "sto",
			[]any{&appliedVersion, &jobID, &jobPath})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: Update: %w", t.label(), err))
			continue
		}
		fmt.Printf("%s: updating to %s (job %d, %s)\n", t.label(), appliedVersion, jobID, jobPath)
	}
	return errors.Join(errs...)
}
