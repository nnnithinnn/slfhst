// Package users creates the admin + svc accounts against the
// installer's mounted target -- systemd-sysusers (declarative
// sysusers.d, not useradd/passwd-file surgery) plus the
// CREDENTIALS_DIRECTORY password mechanism, per the user's explicit
// correction to an earlier draft of this project's plan. Verified
// directly against a real target (see the plan's Phase 0 notes): none
// of this needs a chroot, systemd-sysusers' --root= flag is native
// support for exactly this use case.
//
// SSH key provisioning has no systemd-native mechanism for a non-root
// user (confirmed via DeepWiki: ssh.authorized_keys.root is a root-only
// special case) -- writing ~/.ssh/authorized_keys directly is the only
// path, not a fallback. Same for `loginctl enable-linger`: no daemon
// exists pre-boot to call, so linger is enabled the way loginctl itself
// actually implements it under the hood -- a marker file under
// /var/lib/systemd/linger/.
package users

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/runx"
)

var dataSubdirs = []string{"stalwart", "vaultwarden", "garage", "traefik", "ente"}

var validUsernameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Create sets up the svc system user and the admin human account
// (password + optional SSH key + wheel membership) against the target
// mounted at root, plus the service user's rootless-podman storage
// config, systemd linger marker, and DataDir ownership.
func Create(root string, cfg map[string]any) error {
	adminUser, _ := cfg["admin_user"].(string)
	if adminUser == "" {
		adminUser = "admin"
	}
	if !validUsernameRE.MatchString(adminUser) {
		return fmt.Errorf("users: invalid admin_user %q", adminUser)
	}
	password, _ := cfg["admin_password"].(string)
	if password == "" {
		return fmt.Errorf("users: no admin_password in config.json -- wizard step didn't complete")
	}
	pubkey, _ := cfg["admin_pubkey"].(string)

	if err := writeSysusersConf(root, adminUser); err != nil {
		return err
	}
	if err := runSysusers(root, adminUser, password); err != nil {
		return err
	}

	adminUID, adminGID, err := lookupUser(root, adminUser)
	if err != nil {
		return err
	}
	svcUID, svcGID, err := lookupUser(root, config.ServiceUser)
	if err != nil {
		return err
	}

	// systemd-sysusers only writes the passwd record's home-directory
	// field -- confirmed via DeepWiki, it does NOT create the directory
	// itself (that's what a tmpfiles.d entry is normally for). Do it
	// explicitly rather than relying on some later MkdirAll (e.g.
	// writeAuthorizedKeys, only called when a pubkey is configured) to
	// create it as an incidental side effect.
	if err := ensureHomeDir(root, adminUser, adminUID, adminGID); err != nil {
		return err
	}
	if err := ensureHomeDir(root, config.ServiceUser, svcUID, svcGID); err != nil {
		return err
	}

	if pubkey != "" {
		if err := writeAuthorizedKeys(root, adminUser, adminUID, adminGID, pubkey); err != nil {
			return err
		}
	}
	if err := ensureContainerStorageDir(root, svcUID, svcGID); err != nil {
		return err
	}
	if err := writeRootlessStorageConf(root, svcUID, svcGID); err != nil {
		return err
	}
	if err := enableLinger(root); err != nil {
		return err
	}
	return chownDataDirs(root, svcUID, svcGID)
}

// writeSysusersConf writes the per-install admin/svc user definitions
// under /etc/sysusers.d/, NOT /usr/lib/sysusers.d/ -- confirmed a real,
// install-blocking bug by actually running the installer end to end
// against a real mounted target: /usr is the read-only, swappable erofs
// usr partition (mounted read-only on purpose, see disks.MountUsr), so
// writing there fails outright with "read-only file system". /etc lives
// on the persistent, writable root partition, which is exactly where a
// per-deployment, host-specific file like this belongs anyway --
// systemd-sysusers itself reads both /usr/lib/sysusers.d/ (static,
// package-provided) and /etc/sysusers.d/ (local override) by design, so
// this needs no other change to still be picked up.
func writeSysusersConf(root, adminUser string) error {
	content := fmt.Sprintf(
		"u %s - \"slfhst service user\" %s /usr/sbin/nologin\nu %s - \"slfhst admin\" /home/%s /bin/bash\nm %s wheel\n",
		config.ServiceUser, config.ServiceHomeDir, adminUser, adminUser, adminUser,
	)
	path := filepath.Join(root, "etc/sysusers.d/slfhst.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("users: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("users: write %s: %w", path, err)
	}
	return nil
}

// runSysusers runs `systemd-sysusers --root=<root>`, feeding the admin
// account's plaintext password via the CREDENTIALS_DIRECTORY mechanism
// (systemd-sysusers hashes it itself -- no crypt(3) implementation
// needed on our side). Confirmed against a real target: a real password
// hash lands in the target's /etc/shadow this way.
func runSysusers(root, adminUser, password string) error {
	credDir, err := os.MkdirTemp("", "slfhst-creds-")
	if err != nil {
		return fmt.Errorf("users: create credentials dir: %w", err)
	}
	defer os.RemoveAll(credDir)

	credPath := filepath.Join(credDir, "passwd.plaintext-password."+adminUser)
	if err := os.WriteFile(credPath, []byte(password), 0o400); err != nil {
		return fmt.Errorf("users: write password credential: %w", err)
	}

	_, err = runx.Run([]string{"systemd-sysusers", "--root=" + root},
		runx.Options{Env: []string{"CREDENTIALS_DIRECTORY=" + credDir}, Capture: true})
	if err != nil {
		return fmt.Errorf("users: systemd-sysusers: %w", err)
	}
	return nil
}

func lookupUser(root, username string) (uid, gid int, err error) {
	path := filepath.Join(root, "etc/passwd")
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("users: open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 4 || fields[0] != username {
			continue
		}
		uid, errU := strconv.Atoi(fields[2])
		gid, errG := strconv.Atoi(fields[3])
		if errU != nil || errG != nil {
			return 0, 0, fmt.Errorf("users: malformed passwd entry for %q in %s", username, path)
		}
		return uid, gid, nil
	}
	return 0, 0, fmt.Errorf("users: user %q not found in %s", username, path)
}

func writeAuthorizedKeys(root, username string, uid, gid int, pubkey string) error {
	pubkey = strings.TrimSpace(pubkey)
	sshDir := filepath.Join(root, "home", username, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("users: mkdir %s: %w", sshDir, err)
	}
	akPath := filepath.Join(sshDir, "authorized_keys")

	existing, _ := os.ReadFile(akPath)
	if !strings.Contains(string(existing), pubkey) {
		f, err := os.OpenFile(akPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("users: open %s: %w", akPath, err)
		}
		_, werr := f.WriteString(pubkey + "\n")
		cerr := f.Close()
		if werr != nil {
			return fmt.Errorf("users: write %s: %w", akPath, werr)
		}
		if cerr != nil {
			return fmt.Errorf("users: close %s: %w", akPath, cerr)
		}
	}

	if err := os.Chmod(akPath, 0o600); err != nil {
		return err
	}
	if err := os.Chown(sshDir, uid, gid); err != nil {
		return fmt.Errorf("users: chown %s: %w", sshDir, err)
	}
	if err := os.Chown(akPath, uid, gid); err != nil {
		return fmt.Errorf("users: chown %s: %w", akPath, err)
	}
	return nil
}

func ensureHomeDir(root, username string, uid, gid int) error {
	home := filepath.Join(root, "home", username)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("users: mkdir %s: %w", home, err)
	}
	if err := os.Chown(home, uid, gid); err != nil {
		return fmt.Errorf("users: chown %s: %w", home, err)
	}
	return nil
}

func ensureContainerStorageDir(root string, svcUID, svcGID int) error {
	dir := filepath.Join(root, "var/lib/containers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("users: mkdir %s: %w", dir, err)
	}
	return recursiveChown(dir, svcUID, svcGID)
}

func writeRootlessStorageConf(root string, svcUID, svcGID int) error {
	content := fmt.Sprintf(
		"[storage]\ndriver = \"overlay\"\ngraphroot = \"/var/lib/containers/storage\"\nrunroot = \"/run/user/%d/containers\"\n",
		svcUID,
	)
	dir := filepath.Join(root, "home", config.ServiceUser, ".config/containers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("users: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "storage.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("users: write %s: %w", path, err)
	}
	return recursiveChown(filepath.Join(root, "home", config.ServiceUser, ".config"), svcUID, svcGID)
}

// enableLinger touches the marker file loginctl's own "enable-linger"
// actually writes under the hood -- there's no logind daemon pre-boot to
// call `loginctl enable-linger` against.
func enableLinger(root string) error {
	dir := filepath.Join(root, "var/lib/systemd/linger")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("users: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, config.ServiceUser)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("users: touch %s: %w", path, err)
	}
	return f.Close()
}

func chownDataDirs(root string, svcUID, svcGID int) error {
	for _, name := range dataSubdirs {
		dir := filepath.Join(root, config.DataDir, name)
		if _, err := os.Stat(dir); err != nil {
			continue // matches users.py: only chown dirs that already exist.
		}
		if err := recursiveChown(dir, svcUID, svcGID); err != nil {
			return err
		}
	}
	return nil
}

func recursiveChown(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}
