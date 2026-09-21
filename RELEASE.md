# Release verification

Release tags build one static binary per supported CPU architecture:

```text
dotpkg-linux-amd64
dotpkg-linux-arm64
```

Each release also publishes `SHA256SUMS`. Verify a pinned release before
installing it into a dotfiles checkout:

```sh
version=v0.13.0
curl --fail --location --remote-name \
  "https://github.com/lukelex/dotpkg/releases/download/${version}/dotpkg-linux-amd64"
curl --fail --location --remote-name \
  "https://github.com/lukelex/dotpkg/releases/download/${version}/SHA256SUMS"
sha256sum --ignore-missing --check SHA256SUMS
chmod 0755 dotpkg-linux-amd64
```

Use the ARM64 asset on ARM64 hosts. The release is libc-independent, but one
ELF cannot run on both CPU architectures. The executable still requires host
tools such as `pacman`, `yay`, `sudo`, and possibly `systemd`. If `yay` is
missing, dotpkg checks out a pinned `yay-git` revision before building it; it
does not build an unpinned moving checkout.

The dotfiles integration must pin `version` and its expected checksum. It must
not download or execute a moving `latest` release.
