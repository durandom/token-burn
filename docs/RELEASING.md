# Releasing

Releases are hosted entirely on GitHub.

## Create a Release

Tag and push:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow builds:

- `token-burn_<version>_darwin_amd64.tar.gz`
- `token-burn_<version>_darwin_arm64.tar.gz`
- `token-burn_<version>_linux_amd64.tar.gz`
- `token-burn_<version>_linux_arm64.tar.gz`
- `checksums.txt`

Each archive contains:

- `token-burn`
- `README.md`
- `LICENSE`

## Install Script

The install script downloads the latest GitHub Release, verifies the checksum
when `shasum` or `sha256sum` is available, and installs the binary to
`${TOKEN_BURN_INSTALL_DIR:-~/.local/bin}`.

```sh
curl -fsSL https://raw.githubusercontent.com/durandom/token-burn/main/scripts/install.sh | sh
```

Environment variables:

```text
TOKEN_BURN_REPO          default: durandom/token-burn
TOKEN_BURN_VERSION       default: latest
TOKEN_BURN_INSTALL_DIR   default: ~/.local/bin
```

## Homebrew

The low-maintenance option is to keep GitHub Releases as the source of truth and
add a formula later.

Options:

- Put a formula in a separate `homebrew-tap` repository.
- Use a release tool such as GoReleaser later to update that tap automatically.
- Avoid a tap until users actually need `brew install durandom/tap/token-burn`.

For now, the install script is the primary prebuilt install path.
