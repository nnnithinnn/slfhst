// Package totp is stage1's SSH hardening + mandatory PAM TOTP
// enrollment. Direct port of totp.py, with two deliberate changes:
//
//   - `fail2ban` is dropped (per the user's systemd-only-tooling
//     decision) in favor of `pam_faillock`, wired directly into the PAM
//     stack below rather than a separate log-scraping daemon.
//     `pam_faillock.so` ships in Fedora's base `pam` package, confirmed
//     present in the spike container, no extra install needed.
//   - No `systemctl reload-or-restart sshd`/`enable --now fail2ban`:
//     this runs pre-boot against the installer's mounted target, so
//     there's no live sshd/fail2ban to restart. `sshd -t` validation
//     still runs, but via a real chroot -- confirmed by testing that
//     `sshd -T -f <target>/etc/ssh/sshd_config` alone does NOT rebase
//     the config's `Include sshd_config.d/*.conf` glob against the
//     target root (it silently reads the *installer's own* drop-ins
//     instead), so this is the one place in stage1 that genuinely needs
//     `chroot`, not a target-path flag.
package totp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/nnnithinnn/slfhst/internal/runx"
)

const sshdTOTPConf = `KbdInteractiveAuthentication yes
PasswordAuthentication no
UsePAM yes
PermitRootLogin no

AuthenticationMethods publickey,keyboard-interactive:pam keyboard-interactive:pam

MaxAuthTries 3
LoginGraceTime 30
MaxStartups 10:30:60

ClientAliveInterval 300
ClientAliveCountMax 2

X11Forwarding no

Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com,aes128-gcm@openssh.com,aes256-ctr,aes192-ctr,aes128-ctr
MACs hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,umac-128-etm@openssh.com
KexAlgorithms curve25519-sha256,curve25519-sha256@libssh.org,diffie-hellman-group16-sha512,diffie-hellman-group18-sha512,ecdh-sha2-nistp521,ecdh-sha2-nistp384,ecdh-sha2-nistp256

LogLevel VERBOSE
`

// pamSSHD wires pam_faillock around the password (pam_unix) step only --
// pubkey users never attempt a password, so they shouldn't be throttled
// on password failures they never made. This means pam_ssh_auth_info's
// jump count changes from the original "[success=1 default=ignore]"
// (skip just pam_unix.so) to "[success=2 default=ignore]" (skip
// pam_unix.so AND the authfail faillock line right after it) -- a
// pubkey-authenticated user skips both and goes straight to TOTP.
// Password-path users: preauth counter check -> pam_unix -> on failure,
// [default=die] records the failure and dies immediately (no TOTP
// prompt for a wrong password) -> on success, TOTP -> authsucc resets
// the counter. Not hands-on login-tested (same caveat totp.py always
// carried for this whole file) -- verify with a real login of both
// paths before trusting this in production.
const pamSSHD = `#%PAM-1.0
auth       required                     pam_faillock.so preauth silent deny=5 unlock_time=3600
auth       required                     pam_sepermit.so
auth       [success=2 default=ignore]   pam_ssh_auth_info.so any_of publickey
auth       required                     pam_unix.so
auth       [default=die]                pam_faillock.so authfail deny=5 unlock_time=3600
auth       required                     pam_google_authenticator.so
auth       required                     pam_faillock.so authsucc deny=5 unlock_time=3600
auth       include      postlogin
account    required     pam_faillock.so
account    required     pam_nologin.so
account    include      password-auth
password   include      password-auth
session    required     pam_selinux.so close
session    required     pam_loginuid.so
session    required     pam_selinux.so open env_params
session    optional     pam_keyinit.so force revoke
session    include      password-auth
session    include      postlogin
`

var successJumpRE = regexp.MustCompile(`success=(\d+)`)

// parseSuccessJump extracts the N from a PAM "[success=N ...]" control
// field -- used by totp_test.go to pin the jump count to the actual
// number of auth lines it needs to skip, so an edit to pamSSHD that
// changes one without the other fails a test instead of silently
// breaking the pubkey-vs-password auth split.
func parseSuccessJump(line string) (int, error) {
	m := successJumpRE.FindStringSubmatch(line)
	if m == nil {
		return 0, fmt.Errorf("totp: no success=N control field in %q", line)
	}
	return strconv.Atoi(m[1])
}

// GenerateHostKeys runs `ssh-keygen -A -f <root>`, generating the
// target's SSH host keys directly under its mounted path -- no chroot
// needed, confirmed by testing. Needed pre-boot since there's no
// sshd-keygen.target to rely on the way there is at a real boot.
func GenerateHostKeys(root string) error {
	sshDir := filepath.Join(root, "etc/ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		return fmt.Errorf("totp: mkdir %s: %w", sshDir, err)
	}
	if _, err := runx.Run([]string{"ssh-keygen", "-A", "-f", root}, runx.Options{Capture: true}); err != nil {
		return fmt.Errorf("totp: ssh-keygen -A: %w", err)
	}
	return nil
}

// EnrollTOTP runs `google-authenticator` directly against the admin
// user's target-path secret file -- no chroot, no runuser, confirmed via
// its own -s flag. QR/secret/scratch codes go straight to the console
// (the installer's own tty), never touch the network.
func EnrollTOTP(root, adminUser string) error {
	secretPath := filepath.Join(root, "home", adminUser, ".google_authenticator")
	if _, err := os.Stat(secretPath); err == nil {
		return nil // already enrolled -- idempotent, matches totp.py.
	}
	_, err := runx.Run([]string{
		"google-authenticator", "-t", "-f", "-d", "-r", "3", "-R", "30", "-W",
		"-s", secretPath,
	}, runx.Options{})
	if err != nil {
		return fmt.Errorf("totp: google-authenticator: %w", err)
	}
	return nil
}

// mainSSHDConfig is the standard Fedora/RHEL default -- a bare Include
// line, with everything else left to drop-ins. Confirmed a real,
// previously-unknown gap by actually running the installer end to end:
// nothing else in this project's split root/usr design ever creates
// this file (openssh-server ships no /usr/share/factory/etc/ssh/
// fallback either, confirmed by checking the real built content, so
// there's no factory-default to fall back on), and without it sshd has
// no config file to read at all -- `sshd -t` failed with "/etc/ssh/
// sshd_config: No such file or directory" against an otherwise
// correctly-generated drop-in.
const mainSSHDConfig = "Include /etc/ssh/sshd_config.d/*.conf\n"

func writeSSHDConfig(root string) error {
	dir := filepath.Join(root, "etc/ssh/sshd_config.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("totp: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "10-slfhst-totp.conf")
	if err := os.WriteFile(path, []byte(sshdTOTPConf), 0o644); err != nil {
		return fmt.Errorf("totp: write %s: %w", path, err)
	}

	mainPath := filepath.Join(root, "etc/ssh/sshd_config")
	if _, err := os.Stat(mainPath); err == nil {
		return nil // already present -- idempotent, don't clobber an existing one.
	}
	if err := os.WriteFile(mainPath, []byte(mainSSHDConfig), 0o644); err != nil {
		return fmt.Errorf("totp: write %s: %w", mainPath, err)
	}
	return nil
}

func writePAMStack(root string) error {
	path := filepath.Join(root, "etc/pam.d/sshd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("totp: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(pamSSHD), 0o644); err != nil {
		return fmt.Errorf("totp: write %s: %w", path, err)
	}
	return nil
}

// ValidateSSHDConfig runs `chroot <root> sshd -t` -- the one place stage1
// genuinely needs a real chroot (see the package doc comment for why
// `sshd -T -f` alone isn't enough).
//
// Bind-mounts the installer's own live /dev onto <root>/dev first, and
// unmounts it afterward -- found to be genuinely necessary, not
// optional, by actually running this against a real freshly-partitioned
// target: /dev is normally populated by the kernel's devtmpfs at real
// boot time, which never happens for a pre-boot chroot, so sshd failed
// with "Couldn't open /dev/null: No such file or directory" against an
// otherwise entirely correct config. Same standard pattern real
// installers (debootstrap, arch-chroot) already use for exactly this
// reason.
func ValidateSSHDConfig(root string) error {
	devPath := filepath.Join(root, "dev")
	if err := os.MkdirAll(devPath, 0o755); err != nil {
		return fmt.Errorf("totp: mkdir %s: %w", devPath, err)
	}
	if _, err := runx.Run([]string{"mount", "--bind", "/dev", devPath}, runx.Options{}); err != nil {
		return fmt.Errorf("totp: bind-mount /dev onto %s: %w", devPath, err)
	}
	defer runx.Run([]string{"umount", devPath}, runx.Options{})

	if _, err := runx.Run([]string{"sshd", "-t"}, runx.Options{Root: root, Capture: true}); err != nil {
		return fmt.Errorf("totp: sshd -t failed against %s: %w", root, err)
	}
	return nil
}

// Configure writes the sshd_config drop-in and PAM stack, generates host
// keys, validates the result, and enrolls the admin user's TOTP secret.
// Direct port of totp.py's main(), minus the live-daemon restart/enable
// calls this design doesn't need pre-boot.
func Configure(root, adminUser string) error {
	if err := GenerateHostKeys(root); err != nil {
		return err
	}
	if err := writeSSHDConfig(root); err != nil {
		return err
	}
	if err := writePAMStack(root); err != nil {
		return err
	}
	if err := ValidateSSHDConfig(root); err != nil {
		return err
	}
	return EnrollTOTP(root, adminUser)
}
