package totp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSSHDConfig(t *testing.T) {
	root := t.TempDir()
	if err := writeSSHDConfig(root); err != nil {
		t.Fatalf("writeSSHDConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/ssh/sshd_config.d/10-slfhst-totp.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PasswordAuthentication no", "PermitRootLogin no", "UsePAM yes"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("sshd_config.d drop-in missing %q", want)
		}
	}

	// Regression test: the main /etc/ssh/sshd_config must exist too, or
	// sshd has nothing to read at all -- confirmed a real gap by
	// actually running the installer end to end (no factory-default
	// ships one either, in this project's split root/usr design).
	main, err := os.ReadFile(filepath.Join(root, "etc/ssh/sshd_config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), "Include /etc/ssh/sshd_config.d/*.conf") {
		t.Errorf("main sshd_config missing the Include line:\n%s", main)
	}
}

// TestPAMStackJumpCountMatchesInsertedRules is the one thing worth
// pinning down with an automated test for a file this security-
// sensitive: pam_ssh_auth_info's "[success=N default=ignore]" jump count
// must equal exactly the number of `auth` lines between it and
// pam_google_authenticator.so (the rules a successfully-pubkey-
// authenticated user should skip: the password attempt and its
// paired authfail record). Catches a stack edit that adds/removes a
// line without updating the jump count -- the kind of mistake that
// either locks everyone out or, worse, silently breaks the skip and lets
// something unintended run.
func TestPAMStackJumpCountMatchesInsertedRules(t *testing.T) {
	lines := strings.Split(pamSSHD, "\n")
	jumpIdx, skipN := -1, 0
	for i, line := range lines {
		if strings.Contains(line, "pam_ssh_auth_info.so") {
			jumpIdx = i
			if _, err := parseSuccessJump(line); err != nil {
				t.Fatalf("parse jump count: %v", err)
			}
			skipN, _ = parseSuccessJump(line)
			break
		}
	}
	if jumpIdx == -1 {
		t.Fatal("pam_ssh_auth_info.so line not found in pamSSHD")
	}

	authAfter := 0
	googleIdx := -1
	for i := jumpIdx + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "auth") {
			if strings.Contains(lines[i], "pam_google_authenticator.so") {
				googleIdx = i
				break
			}
			authAfter++
		}
	}
	if googleIdx == -1 {
		t.Fatal("pam_google_authenticator.so line not found after pam_ssh_auth_info.so")
	}
	if authAfter != skipN {
		t.Errorf("pam_ssh_auth_info success=%d, but %d auth line(s) sit between it and pam_google_authenticator.so -- these must match", skipN, authAfter)
	}
}

func TestWritePAMStack(t *testing.T) {
	root := t.TempDir()
	if err := writePAMStack(root); err != nil {
		t.Fatalf("writePAMStack: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/pam.d/sshd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pam_faillock.so", "pam_ssh_auth_info.so", "pam_google_authenticator.so", "pam_unix.so"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("PAM stack missing %q", want)
		}
	}
}
