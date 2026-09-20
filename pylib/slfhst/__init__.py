"""slfhst: the appliance's first-boot stages and ongoing ops CLI.

Everything here is plain Python 3 stdlib -- no shell scripts, no pip
dependencies baked into the image. External tools (parted, firewall-cmd,
podman, systemctl, google-authenticator, ...) are invoked via subprocess
with argv lists, never through a shell.
"""
