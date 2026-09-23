// Package runx is the single chokepoint every other slfhst package uses
// to run external commands -- always argv-based, never through a shell.
// Direct port of common.py's run() plus a new chroot-exec helper needed
// for the installer's stage1-in-chroot work (no Python precedent).
package runx

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Options configures a single Run call. The zero value runs the command
// as-is, inheriting stdio (matches capture=False in common.py -- needed
// so interactive prompts/TOTP enrollment reach the real console).
type Options struct {
	// AsUser runs the command as this user via `runuser -u <user> --`.
	AsUser string
	// Root runs the command inside `chroot <Root> <argv...>`. Empty
	// means no chroot. Combine with AsUser to chroot-then-runuser.
	Root string
	// Input, if non-nil, is piped to the command's stdin instead of
	// inheriting the parent's.
	Input io.Reader
	// Capture, if true, buffers stdout/stderr instead of inheriting
	// the parent's -- matches capture=True in common.py.
	Capture bool
	// NoCheck, if true, makes a non-zero exit NOT an error -- matches
	// check=False in common.py (existence probes like `podman secret
	// inspect`, `firewall-cmd --query...`, etc., where the caller reads
	// Result.ExitCode themselves rather than treating "not found" as a
	// failure).
	NoCheck bool
	// Env holds extra "KEY=VALUE" environment variables, appended to
	// the current process's environment (e.g. CREDENTIALS_DIRECTORY for
	// systemd-sysusers' credential mechanism).
	Env []string
}

// Result mirrors the pieces of Python's subprocess.CompletedProcess this
// project actually reads.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes argv (never through a shell) per opts. By default a
// non-zero exit is returned as an error, matching common.py's check=True
// default -- pass a *bool via opts if a caller genuinely needs
// check=False semantics (inspect Result.ExitCode instead).
func Run(argv []string, opts Options) (Result, error) {
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("runx: empty argv")
	}

	full := buildArgv(argv, opts)
	cmd := exec.Command(full[0], full[1:]...)
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}

	if opts.Input != nil {
		cmd.Stdin = opts.Input
	} else {
		cmd.Stdin = os.Stdin
	}

	var stdout, stderr bytes.Buffer
	if opts.Capture {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		if opts.NoCheck {
			if _, isExitErr := err.(*exec.ExitError); isExitErr {
				return res, nil
			}
		}
		return res, fmt.Errorf("runx: %v: %w (stderr: %s)", full, err, res.Stderr)
	}
	return res, nil
}

// RunChecked is Run with Capture forced true, for the common
// "run it, check it succeeded, read stdout" pattern.
func RunChecked(argv []string, opts Options) (string, error) {
	opts.Capture = true
	res, err := Run(argv, opts)
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

func buildArgv(argv []string, opts Options) []string {
	full := argv
	if opts.AsUser != "" {
		full = append([]string{"runuser", "-u", opts.AsUser, "--"}, full...)
	}
	if opts.Root != "" {
		full = append([]string{"chroot", opts.Root}, full...)
	}
	return full
}
