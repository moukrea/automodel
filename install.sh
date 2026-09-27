#!/bin/sh
# automodel installer:
#   curl -fsSL https://raw.githubusercontent.com/moukrea/automodel/main/install.sh | sh
# Installs the latest release in ~/.local/bin (AUTOMODEL_BIN_DIR), wires it
# into Claude Code (`automodel install`) and asks for the OpenRouter key
# (or takes $OPENROUTER_API_KEY). Pin a version with AUTOMODEL_VERSION=v0.1.0.
set -eu

REPO="moukrea/automodel"
BIN_DIR="${AUTOMODEL_BIN_DIR:-$HOME/.local/bin}"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
fail() { printf 'automodel install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need curl
need tar

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux | darwin) ;; *) fail "unsupported OS: $os" ;; esac
arch=$(uname -m)
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported CPU: $arch" ;;
esac

tag="${AUTOMODEL_VERSION:-}"
if [ -z "$tag" ]; then
	tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
fi
[ -n "$tag" ] || fail "could not find the latest release"
ver=${tag#v}
file="automodel_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "Downloading automodel $tag ($os/$arch)"
curl -fsSL "$base/$file" -o "$tmp/$file"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
want=$(grep " $file\$" "$tmp/checksums.txt" | cut -d ' ' -f 1)
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$file" | cut -d ' ' -f 1)
else
	got=$(shasum -a 256 "$tmp/$file" | cut -d ' ' -f 1)
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "checksum mismatch for $file"
tar -xzf "$tmp/$file" -C "$tmp" automodel
mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/automodel" "$BIN_DIR/automodel"
say "Installed $BIN_DIR/automodel"

AUTOMODEL_INSTALLER=1 "$BIN_DIR/automodel" install

cfg="${AUTOMODEL_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/automodel/config.toml}"
if [ -n "${OPENROUTER_API_KEY:-}" ]; then
	printf '%s\n' "$OPENROUTER_API_KEY" | "$BIN_DIR/automodel" key set
elif ! grep -q '^openrouter_api_key = "..*"' "$cfg" 2>/dev/null && (: </dev/tty) 2>/dev/null; then
	printf 'OpenRouter API key for Jev (hidden, empty to skip): ' >/dev/tty
	stty -echo </dev/tty 2>/dev/null || true
	read -r key </dev/tty || key=""
	stty echo </dev/tty 2>/dev/null || true
	printf '\n' >/dev/tty
	if [ -n "$key" ]; then printf '%s\n' "$key" | "$BIN_DIR/automodel" key set; fi
fi

grep -q '^openrouter_api_key = "..*"' "$cfg" 2>/dev/null || [ -n "${OPENROUTER_API_KEY:-}" ] ||
	say "No OpenRouter key yet: until you run \`automodel key set\`, every prompt uses the default tier."
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) say "Add $BIN_DIR to your PATH." ;; esac
say 'Done. In Claude Code, open /model and pick "Jev (auto)".'
