// Package sshauth is stage1's SSH hardening: password auth, pam_faillock
// lockout, host key generation, and sshd_config validation.
package sshauth

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nnnithinnn/slfhst/internal/runx"
)

const (
	faillockDeny       = 3
	faillockUnlockTime = 86400
)

const sshdConf = `PasswordAuthentication yes
UsePAM yes
PermitRootLogin no

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

var pamSSHD = fmt.Sprintf(`#%%PAM-1.0
auth       required                     pam_faillock.so preauth silent deny=%d unlock_time=%d
auth       required                     pam_sepermit.so
auth       required                     pam_unix.so
auth       [default=die]                pam_faillock.so authfail deny=%d unlock_time=%d
auth       required                     pam_faillock.so authsucc deny=%d unlock_time=%d
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
`, faillockDeny, faillockUnlockTime, faillockDeny, faillockUnlockTime, faillockDeny, faillockUnlockTime)

func GenerateHostKeys(root string) error {
	sshDir := filepath.Join(root, "etc/ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		return fmt.Errorf("sshauth: mkdir %s: %w", sshDir, err)
	}
	if _, err := runx.Run([]string{"ssh-keygen", "-A", "-f", root}, runx.Options{Capture: true}); err != nil {
		return fmt.Errorf("sshauth: ssh-keygen -A: %w", err)
	}
	return nil
}

const mainSSHDConfig = "Include /etc/ssh/sshd_config.d/*.conf\n"

func writeSSHDConfig(root string) error {
	dir := filepath.Join(root, "etc/ssh/sshd_config.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sshauth: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "10-slfhst.conf")
	if err := os.WriteFile(path, []byte(sshdConf), 0o644); err != nil {
		return fmt.Errorf("sshauth: write %s: %w", path, err)
	}

	mainPath := filepath.Join(root, "etc/ssh/sshd_config")
	if _, err := os.Stat(mainPath); err == nil {
		return nil
	}
	if err := os.WriteFile(mainPath, []byte(mainSSHDConfig), 0o644); err != nil {
		return fmt.Errorf("sshauth: write %s: %w", mainPath, err)
	}
	return nil
}

func writePAMStack(root string) error {
	path := filepath.Join(root, "etc/pam.d/sshd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sshauth: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(pamSSHD), 0o644); err != nil {
		return fmt.Errorf("sshauth: write %s: %w", path, err)
	}
	return nil
}

// ValidateSSHDConfig runs `chroot <root> sshd -t` -- `sshd -T -f` alone
// does not rebase the config's `Include sshd_config.d/*.conf` glob
// against the target root, so a real chroot is required here.
func ValidateSSHDConfig(root string) error {
	devPath := filepath.Join(root, "dev")
	if err := os.MkdirAll(devPath, 0o755); err != nil {
		return fmt.Errorf("sshauth: mkdir %s: %w", devPath, err)
	}
	if _, err := runx.Run([]string{"mount", "--bind", "/dev", devPath}, runx.Options{}); err != nil {
		return fmt.Errorf("sshauth: bind-mount /dev onto %s: %w", devPath, err)
	}
	defer runx.Run([]string{"umount", devPath}, runx.Options{})

	if _, err := runx.Run([]string{"sshd", "-t"}, runx.Options{Root: root, Capture: true}); err != nil {
		return fmt.Errorf("sshauth: sshd -t failed against %s: %w", root, err)
	}
	return nil
}

func Configure(root string) error {
	if err := GenerateHostKeys(root); err != nil {
		return err
	}
	if err := writeSSHDConfig(root); err != nil {
		return err
	}
	if err := writePAMStack(root); err != nil {
		return err
	}
	return ValidateSSHDConfig(root)
}
