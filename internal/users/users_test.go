package users

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakePasswd(t *testing.T, root string, entries map[string][2]int) {
	t.Helper()
	var b strings.Builder
	for name, ids := range entries {
		b.WriteString(name)
		b.WriteString(":x:")
		b.WriteString(itoa(ids[0]))
		b.WriteString(":")
		b.WriteString(itoa(ids[1]))
		b.WriteString(":::/usr/sbin/nologin\n")
	}
	path := filepath.Join(root, "etc/passwd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestLookupUser(t *testing.T) {
	root := t.TempDir()
	writeFakePasswd(t, root, map[string][2]int{"svc": {998, 998}, "admin": {997, 997}})

	uid, gid, err := lookupUser(root, "svc")
	if err != nil {
		t.Fatalf("lookupUser: %v", err)
	}
	if uid != 998 || gid != 998 {
		t.Errorf("got (%d,%d), want (998,998)", uid, gid)
	}

	if _, _, err := lookupUser(root, "nonexistent"); err == nil {
		t.Error("expected an error for a nonexistent user")
	}
}

func TestWriteSysusersConf(t *testing.T) {
	root := t.TempDir()
	if err := writeSysusersConf(root, "admin"); err != nil {
		t.Fatalf("writeSysusersConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/sysusers.d/slfhst.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"u svc - \"slfhst service user\" /home/svc /usr/sbin/nologin",
		"u admin - \"slfhst admin\" /home/admin /bin/bash",
		"m admin wheel",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("sysusers.conf missing %q:\n%s", want, text)
		}
	}
}

func TestCreateRejectsInvalidUsername(t *testing.T) {
	root := t.TempDir()
	err := Create(root, map[string]any{"admin_user": "not a valid user!", "admin_password": "x"})
	if err == nil {
		t.Fatal("expected an error for an invalid admin_user")
	}
}

func TestCreateRequiresPassword(t *testing.T) {
	root := t.TempDir()
	err := Create(root, map[string]any{"admin_user": "admin"})
	if err == nil {
		t.Fatal("expected an error when admin_password is missing")
	}
}

func TestWriteAuthorizedKeysIdempotent(t *testing.T) {
	root := t.TempDir()
	// chown to the test process's own uid/gid -- os.Chown to an
	// arbitrary uid requires CAP_CHOWN (this test may run
	// unprivileged), but chowning a file to yourself is always
	// permitted. A hardcoded 1000 here worked locally by coincidence
	// (that sandbox's own user happened to be uid 1000) and failed for
	// real on GitHub's runner (a different uid) -- same fix already
	// applied correctly in TestWriteRootlessStorageConf, missed here.
	uid, gid := os.Getuid(), os.Getgid()
	writeFakePasswd(t, root, map[string][2]int{"admin": {uid, gid}})
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI test@example"

	if err := writeAuthorizedKeys(root, "admin", uid, gid, key); err != nil {
		t.Fatalf("writeAuthorizedKeys: %v", err)
	}
	if err := writeAuthorizedKeys(root, "admin", uid, gid, key); err != nil {
		t.Fatalf("writeAuthorizedKeys (second call): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "home/admin/.ssh/authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), key); got != 1 {
		t.Errorf("key appears %d times, want 1 (idempotent append)", got)
	}
}

func TestWriteRootlessStorageConf(t *testing.T) {
	root := t.TempDir()
	// chown to the test process's own uid/gid -- os.Chown to an
	// arbitrary uid requires CAP_CHOWN (this test may run
	// unprivileged), but chowning a file to yourself is always
	// permitted and still exercises the real chown call, just not with
	// a fabricated uid.
	uid, gid := os.Getuid(), os.Getgid()
	writeFakePasswd(t, root, map[string][2]int{"svc": {uid, gid}})
	if err := writeRootlessStorageConf(root, uid, gid); err != nil {
		t.Fatalf("writeRootlessStorageConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "home/svc/.config/containers/storage.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("/run/user/%d/containers", uid)
	if !strings.Contains(string(data), want) {
		t.Errorf("storage.conf missing runroot %q:\n%s", want, data)
	}
}

func TestEnableLinger(t *testing.T) {
	root := t.TempDir()
	if err := enableLinger(root); err != nil {
		t.Fatalf("enableLinger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/systemd/linger/svc")); err != nil {
		t.Errorf("linger marker not created: %v", err)
	}
}

func TestChownDataDirsSkipsMissing(t *testing.T) {
	root := t.TempDir()
	// None of the dataSubdirs exist under root -- should be a silent
	// no-op, not an error, matching users.py.
	if err := chownDataDirs(root, 998, 998); err != nil {
		t.Fatalf("chownDataDirs on an empty tree: %v", err)
	}
}
