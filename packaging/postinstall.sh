#!/bin/sh
# deb/rpm post-install: automodel is set up per user, not by the package.
# deb: "configure" [old version]; rpm: 1 on install, 2+ on upgrade.
case "${1:-}:${2:-}" in
configure: | 1:)
	echo 'automodel installed. As your user (not root), run `automodel install`'
	echo 'to start its proxy and wire it into Claude Code.'
	;;
*)
	echo 'automodel upgraded: running proxies restart on the new binary within a minute.'
	;;
esac
exit 0
