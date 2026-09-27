#!/bin/sh
# Prints the Homebrew formula of a release from its checksums.txt:
#   scripts/homebrew-formula.sh 0.2.0 dist/checksums.txt >Formula/automodel.rb
set -eu
ver=$1
sums=$2

sha() {
	s=$(awk -v f="automodel_${ver}_$1.tar.gz" '$2 == f { print $1 }' "$sums")
	[ -n "$s" ] || { echo "no automodel_${ver}_$1.tar.gz in $sums" >&2; exit 1; }
	echo "$s"
}

block() { # OS ARCH
	cat <<EOF
      url "https://github.com/moukrea/automodel/releases/download/v$ver/automodel_${ver}_$1_$2.tar.gz"
      sha256 "$(sha "$1_$2")"
EOF
}

# Fail before printing anything when an archive is missing.
for p in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do sha $p >/dev/null; done

cat <<EOF
class Automodel < Formula
  desc "Picks the model and effort for each Claude Code prompt, via TypeSafe Jev"
  homepage "https://github.com/moukrea/automodel"
  version "$ver"

  on_macos do
    on_intel do
$(block darwin amd64)
    end
    on_arm do
$(block darwin arm64)
    end
  end

  on_linux do
    on_intel do
$(block linux amd64)
    end
    on_arm do
$(block linux arm64)
    end
  end

  def install
    bin.install "automodel"
  end

  def caveats
    <<~EOS
      Run \`automodel install\` to start the proxy and wire automodel into Claude Code.
      Homebrew upgrades it: \`brew upgrade automodel\` (the proxy restarts by itself).
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/automodel version")
  end
end
EOF
