#!/usr/bin/env sh
set -eu

repo="${TOKEN_BURN_REPO:-durandom/token-burn}"
version="${TOKEN_BURN_VERSION:-latest}"
install_dir="${TOKEN_BURN_INSTALL_DIR:-$HOME/.local/bin}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "$os" in
  darwin|linux) ;;
  *)
    echo "unsupported OS: $os" >&2
    exit 1
    ;;
esac

case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

if [ "$version" = "latest" ]; then
  api_url="https://api.github.com/repos/$repo/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    version="$(curl -fsSL "$api_url" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  elif command -v wget >/dev/null 2>&1; then
    version="$(wget -qO- "$api_url" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
fi

if [ -z "$version" ]; then
  echo "could not resolve release version" >&2
  exit 1
fi

asset="token-burn_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$repo/releases/download/$version"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

download() {
  url="$1"
  dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$dest"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

download "$base_url/$asset" "$tmp_dir/$asset"
download "$base_url/checksums.txt" "$tmp_dir/checksums.txt"

if command -v shasum >/dev/null 2>&1; then
  expected="$(grep "  $asset\$" "$tmp_dir/checksums.txt" | awk '{print $1}')"
  actual="$(shasum -a 256 "$tmp_dir/$asset" | awk '{print $1}')"
  if [ "$expected" != "$actual" ]; then
    echo "checksum mismatch for $asset" >&2
    exit 1
  fi
elif command -v sha256sum >/dev/null 2>&1; then
  expected="$(grep "  $asset\$" "$tmp_dir/checksums.txt" | awk '{print $1}')"
  actual="$(sha256sum "$tmp_dir/$asset" | awk '{print $1}')"
  if [ "$expected" != "$actual" ]; then
    echo "checksum mismatch for $asset" >&2
    exit 1
  fi
else
  echo "warning: shasum or sha256sum not found; skipping checksum verification" >&2
fi

mkdir -p "$install_dir"
tar -xzf "$tmp_dir/$asset" -C "$tmp_dir"
cp "$tmp_dir/token-burn_${version}_${os}_${arch}/token-burn" "$install_dir/token-burn"
chmod 0755 "$install_dir/token-burn"

echo "installed token-burn $version to $install_dir/token-burn"
echo "run: $install_dir/token-burn version"
