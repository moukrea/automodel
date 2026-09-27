#!/bin/sh
# End-to-end test of install.sh with the binary built from the working tree
# (served as a release archive + checksums.txt through AUTOMODEL_BASE_URL).
#   scripts/install-test.sh IMAGE   in a clean container, e.g. debian:stable-slim
#                                   (podman or docker; $CONTAINER_ENGINE picks one)
#   scripts/install-test.sh --host  on this machine: CI runners only, it
#                                   installs into $HOME and then uninstalls
set -eu

ver=0.0.0-test
root=$(cd "$(dirname "$0")/.." && pwd)
listen=127.0.0.1:8788

fail() { printf 'install test: %s\n' "$*" >&2; exit 1; }

# build GOOS DIR: the release archive for this CPU and its checksums.txt.
build() {
	arch=$(go env GOARCH)
	CGO_ENABLED=0 GOOS=$1 go build -C "$root" -ldflags "-X main.version=$ver" -o "$2/automodel" ./cmd/automodel
	tar -czf "$2/automodel_${ver}_$1_$arch.tar.gz" -C "$2" automodel
	rm "$2/automodel"
	cd "$2"
	if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz; else shasum -a 256 ./*.tar.gz; fi |
		sed 's| \./| |' >checksums.txt
	cd - >/dev/null
}

up() { curl -fsS "http://$listen/automodel/health" 2>/dev/null; }

# check DIST: install.sh, then doctor, settings, proxy, start, uninstall.
check() {
	case $(uname -s) in Darwin) mode=launchd ;; *) mode=detached ;; esac
	AUTOMODEL_VERSION=v$ver AUTOMODEL_BASE_URL=file://$1 sh "$root/install.sh" </dev/null | tee /tmp/automodel-install.out
	if [ $mode = detached ]; then
		grep -q 'will NOT be restarted after a reboot' /tmp/automodel-install.out || fail "no reboot notice"
		grep -q "automodel --config .* start >/dev/null 2>&1" /tmp/automodel-install.out || fail "no login snippet"
	fi
	bin=$HOME/.local/bin/automodel
	"$bin" doctor | tee /tmp/automodel-doctor.out || fail "doctor reported a failure"
	grep -q "service .*$mode" /tmp/automodel-doctor.out || fail "service is not $mode"
	grep -q "\"ANTHROPIC_BASE_URL\": \"http://$listen\"" "$HOME/.claude/settings.json" || fail "settings.json not written"
	up | grep -q "\"version\":\"$ver\"" || fail "proxy not answering on $listen"
	"$bin" start | grep -q 'already running' || fail "start restarted a running proxy"
	if [ $mode = detached ]; then
		# The hooks' guard relaunches a background proxy that died.
		kill "$(cat "$HOME/.local/state/automodel/proxy.pid")"
		sleep 1
		! up >/dev/null || fail "proxy survived kill"
		cfg=$HOME/.config/automodel/config.toml
		echo '{"session_id":"t"}' | "$bin" --config "$cfg" hook session-start >/dev/null
		up >/dev/null || fail "session-start hook did not relaunch the proxy"
	fi
	"$bin" uninstall
	i=0
	while up >/dev/null; do
		i=$((i + 1))
		[ $i -lt 50 ] || fail "proxy still up after uninstall"
		sleep 0.1
	done
	! grep -q ANTHROPIC_BASE_URL "$HOME/.claude/settings.json" || fail "settings not restored"
	echo "install test passed ($mode)"
}

case ${1:-} in
"") sed -n '2,7p' "$0" >&2 && exit 2 ;;
--in-container)
	missing=
	for c in curl tar gzip; do command -v $c >/dev/null 2>&1 || missing="$missing $c"; done
	if [ -n "$missing" ]; then
		if command -v apt-get >/dev/null; then
			export DEBIAN_FRONTEND=noninteractive
			apt-get update -qq && apt-get install -y -qq $missing ca-certificates >/dev/null
		else
			dnf install -y -q $missing >/dev/null
		fi
	fi
	check /dist
	;;
--host)
	dist=$(mktemp -d)
	build "$(go env GOOS)" "$dist"
	check "$dist"
	;;
*)
	engine=${CONTAINER_ENGINE:-$(command -v podman || command -v docker)}
	dist=$(mktemp -d)
	trap 'rm -rf "$dist"' EXIT
	build linux "$dist"
	"$engine" run --rm --init -v "$root:/src:ro,z" -v "$dist:/dist:ro,z" "$1" sh /src/scripts/install-test.sh --in-container
	;;
esac
