#!/usr/bin/env sh
# kaptanto install script
# Usage: curl -fsSL https://get.kaptan.to | sh
#    Or: curl -fsSL https://get.kaptan.to | KAPTANTO_VERSION=v0.2.0 sh
#    Or: curl -fsSL https://get.kaptan.to | sh -s -- --version v0.2.0

set -eu

REPO="kaptanto/kaptanto"
BINARY="kaptanto"
INSTALL_DIR="/usr/local/bin"

# Allow pinning a specific version via env var or --version flag.
VERSION="${KAPTANTO_VERSION:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    *) shift ;;
  esac
done

# Resolve the latest release tag if no version was specified.
if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed 's/.*"tag_name": *"\(.*\)".*/\1/')
fi

if [ -z "$VERSION" ]; then
  echo "error: could not determine latest version" >&2
  exit 1
fi

# Detect operating system.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)
    echo "error: unsupported OS: $OS" >&2
    exit 1
    ;;
esac

# Detect CPU architecture.
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

ARCHIVE="${BINARY}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
CHECKSUMS="${BINARY}_checksums.txt"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading kaptanto ${VERSION} (${OS}/${ARCH})..."
curl -fsSL "${BASE_URL}/${ARCHIVE}"   -o "${TMP}/${ARCHIVE}"
curl -fsSL "${BASE_URL}/${CHECKSUMS}" -o "${TMP}/${CHECKSUMS}"

# Verify checksum before extracting.
cd "$TMP"
if command -v sha256sum >/dev/null 2>&1; then
  grep "${ARCHIVE}" "${CHECKSUMS}" | sha256sum -c -
elif command -v shasum >/dev/null 2>&1; then
  grep "${ARCHIVE}" "${CHECKSUMS}" | shasum -a 256 -c -
else
  echo "warning: sha256sum/shasum not found; skipping checksum verification" >&2
fi
cd - >/dev/null

# Extract binary from archive.
tar -xzf "${TMP}/${ARCHIVE}" -C "${TMP}" "${BINARY}"

# Install to /usr/local/bin, falling back to $HOME/.local/bin if no write permission.
if [ -w "$INSTALL_DIR" ]; then
  mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
  chmod +x "${INSTALL_DIR}/${BINARY}"
elif command -v sudo >/dev/null 2>&1; then
  sudo mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
  sudo chmod +x "${INSTALL_DIR}/${BINARY}"
else
  INSTALL_DIR="${HOME}/.local/bin"
  mkdir -p "$INSTALL_DIR"
  mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
  chmod +x "${INSTALL_DIR}/${BINARY}"
  echo "Installed to ${INSTALL_DIR}. Ensure it is on your PATH."
fi

echo ""
echo "kaptanto ${VERSION} installed to ${INSTALL_DIR}/${BINARY}"
"${INSTALL_DIR}/${BINARY}" version
