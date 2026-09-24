package sshauth

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
	data, err := os.ReadFile(filepath.Join(root, "etc/ssh/sshd_config.d/10-slfhst.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PasswordAuthentication yes", "PermitRootLogin no", "UsePAM yes"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("sshd_config.d drop-in missing %q", want)
		}
	}
	if strings.Contains(string(data), "AuthenticationMethods") {
		t.Errorf("sshd_config.d drop-in should not force AuthenticationMethods")
	}

	main, err := os.ReadFile(filepath.Join(root, "etc/ssh/sshd_config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), "Include /etc/ssh/sshd_config.d/*.conf") {
		t.Errorf("main sshd_config missing the Include line:\n%s", main)
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
	for _, want := range []string{"pam_faillock.so", "pam_unix.so"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("PAM stack missing %q", want)
		}
	}
	for _, unwanted := range []string{"pam_ssh_auth_info.so", "pam_google_authenticator.so"} {
		if strings.Contains(string(data), unwanted) {
			t.Errorf("PAM stack should not contain %q", unwanted)
		}
	}
}
