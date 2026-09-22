"""Stage1: SSH hardening -- optional key, always mandatory PAM TOTP,
console-only enrollment. A password is always a valid first factor
(users.py sets one for every admin account, whether or not a key is
also configured); a key, when present, lets TOTP be the only other
factor needed instead of key+password+TOTP -- see PAM_SSHD_CONTENT.

The QR code / secret / scratch codes are printed ONLY to the physical
console (this unit owns tty1, same as the wizard) and never touch the
network, so there's no TOFU-over-SSH exposure for the second factor itself.
"""
from __future__ import annotations

from pathlib import Path
from textwrap import dedent

from . import common
from .common import log, run


# Numbered to sort BEFORE AlmaLinux's own drop-ins (40-redhat-crypto-
# policies.conf, 50-redhat.conf), not after -- found by actually testing
# with `sshd -t` rather than assuming: sshd_config's Include mechanism is
# first-occurrence-wins for most directives (not last, unlike most config
# systems), so a higher-numbered file here would have its
# KbdInteractiveAuthentication/ChallengeResponseAuthentication and
# X11Forwarding silently overridden by 50-redhat.conf's own settings, and
# its Ciphers/MACs/KexAlgorithms silently ignored entirely -- 40-redhat-
# crypto-policies.conf's own comment says as much: override those "before
# this block," i.e. in an earlier-sorting file.
SSHD_DROPIN = Path("/etc/ssh/sshd_config.d/10-slfhst-totp.conf")
PAM_SSHD = Path("/etc/pam.d/sshd")
FAIL2BAN_JAIL = Path("/etc/fail2ban/jail.d/sshd.local")

SSHD_DROPIN_CONTENT = dedent("""\
    KbdInteractiveAuthentication yes
    PasswordAuthentication no
    UsePAM yes
    PermitRootLogin no

    # Key-based auth is optional (users.py doesn't require an admin pubkey
    # any more): "publickey,keyboard-interactive:pam" and bare
    # "keyboard-interactive:pam" are alternative method-sets -- either
    # satisfies AuthenticationMethods. TOTP is mandatory either way, via
    # keyboard-interactive:pam's PAM stack (see PAM_SSHD_CONTENT) -- what
    # differs is whether that stack also demands a Unix password first,
    # which it does UNLESS pubkey already succeeded (pam_ssh_auth_info.so).
    AuthenticationMethods publickey,keyboard-interactive:pam keyboard-interactive:pam

    # Reduce the brute-force/resource-exhaustion budget: fewer auth
    # attempts per connection, less time to complete auth, and cap
    # concurrent *unauthenticated* connections -- a connection flood
    # doesn't need valid credentials to make sshd fork auth processes,
    # so this matters even with key+TOTP auth. GreenCloudVPS's own
    # network-edge DDoS scrubbing doesn't reach this layer.
    MaxAuthTries 3
    LoginGraceTime 30
    MaxStartups 10:30:60

    # Idle sessions get dropped rather than held open indefinitely.
    ClientAliveInterval 300
    ClientAliveCountMax 2

    # Not needed on a headless appliance.
    X11Forwarding no

    # AllowTcpForwarding is deliberately left at its default (enabled):
    # SSH tunneling to reach internal-only services (Garage's S3 API,
    # Postgres, etc. -- nothing on the podman network is exposed
    # publicly) is a legitimate, expected admin workflow here.

    # Modern algorithms only (Mozilla "modern" OpenSSH profile) --
    # drops legacy ciphers/MACs/KEX with known weaknesses.
    Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com,aes128-gcm@openssh.com,aes256-ctr,aes192-ctr,aes128-ctr
    MACs hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,umac-128-etm@openssh.com
    KexAlgorithms curve25519-sha256,curve25519-sha256@libssh.org,diffie-hellman-group16-sha512,diffie-hellman-group18-sha512,ecdh-sha2-nistp521,ecdh-sha2-nistp384,ecdh-sha2-nistp256

    LogLevel VERBOSE
    """)

# Stock RHEL/AlmaLinux sshd PAM stack, with the `auth substack password-auth`
# line replaced by: skip-if-pubkey-already-succeeded, else require the Unix
# password, then always require TOTP either way.
#
# pam_ssh_auth_info.so (github.com/eehakkin/pam-ssh-auth-info, built from
# source -- see Containerfile, no EL10/EPEL10 package exists yet) is what
# makes "pubkey skips the password prompt, no pubkey means password is
# required" actually possible: plain PAM has no way to see which SSH
# AuthenticationMethods alternative is being satisfied (confirmed via
# research, not assumed -- "there is no way to make PAM not ask for a
# password ... when a key is correct" is a real, documented OpenSSH/PAM
# limitation without a module like this one exposing SSH_AUTH_INFO_0 to
# PAM). `any_of publickey` + `[success=1 default=ignore]` means: if
# publickey already succeeded, skip the next line (the Unix password
# check); otherwise fall through to it. TOTP is unconditional either way.
#
# Least-verified part of this whole redesign -- no hands-on test in this
# sandbox. Confirm with `sshd -t`/`sshd -T` AND a real login test of BOTH
# paths (key-only, and password-only with no key configured) before
# trusting this, same discipline that caught the real sshd_config.d
# ordering bug earlier in this project's history.
PAM_SSHD_CONTENT = dedent("""\
    #%PAM-1.0
    auth       required                     pam_sepermit.so
    auth       [success=1 default=ignore]   pam_ssh_auth_info.so any_of publickey
    auth       required                     pam_unix.so
    auth       required                     pam_google_authenticator.so
    auth       include      postlogin
    account    required     pam_nologin.so
    account    include      password-auth
    password   include      password-auth
    session    required     pam_selinux.so close
    session    required     pam_loginuid.so
    session    required     pam_selinux.so open env_params
    session    optional     pam_keyinit.so force revoke
    session    include      password-auth
    session    include      postlogin
    """)

FAIL2BAN_JAIL_CONTENT = dedent("""\
    [sshd]
    enabled = true
    backend = systemd
    maxretry = 5
    findtime = 10m
    bantime = 1h
    """)


def _enroll_totp(admin_user: str) -> None:
    home_secret = Path(f"/home/{admin_user}/.google_authenticator")
    if home_secret.exists():
        log.info("TOTP already enrolled for %s", admin_user)
        return
    print()
    print("=" * 70)
    print(f"TOTP enrollment for '{admin_user}' -- scan this now, it is shown once:")
    print("=" * 70)
    run(
        ["google-authenticator", "-t", "-f", "-d", "-r", "3", "-R", "30", "-W"],
        as_user=admin_user,
    )


def _configure_sshd() -> None:
    SSHD_DROPIN.parent.mkdir(parents=True, exist_ok=True)
    SSHD_DROPIN.write_text(SSHD_DROPIN_CONTENT)
    PAM_SSHD.write_text(PAM_SSHD_CONTENT)
    run(["sshd", "-t"])  # validate before reloading
    run(["systemctl", "reload-or-restart", "sshd.service"])


def _configure_fail2ban() -> None:
    FAIL2BAN_JAIL.parent.mkdir(parents=True, exist_ok=True)
    FAIL2BAN_JAIL.write_text(FAIL2BAN_JAIL_CONTENT)
    run(["systemctl", "enable", "--now", "fail2ban.service"])


def main() -> None:
    cfg = common.load_config()
    admin_user = cfg.get("admin_user", "admin")
    _enroll_totp(admin_user)
    _configure_sshd()
    _configure_fail2ban()
    log.info("SSH hardened: password (or key, if configured) + mandatory TOTP, fail2ban active")


if __name__ == "__main__":
    main()
