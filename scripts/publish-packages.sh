#!/bin/sh
# Publishes a release built by GoReleaser to the Homebrew tap
# (moukrea/homebrew-tap), the apt repository (moukrea/apt-repo) and the rpm
# repository (moukrea/rpm-repo), as the owner's other tools do:
#   scripts/publish-packages.sh VERSION DIST
# Each part is skipped, with a notice, when its secrets are missing:
# RELEASE_TOKEN (push to those repos), GPG_PRIVATE_KEY and GPG_KEY_ID (sign
# the apt and rpm metadata and the rpm packages).
# Dry run: REPO_BASE=<dir or URL holding the three repos> PUSH=0, and
# WORK_DIR=<empty dir> to keep the resulting clones.
set -eu
ver=$1
dist=$(cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
work=${WORK_DIR:-}
if [ -z "$work" ]; then
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
fi

notice() { echo "::notice::$*"; }

if [ -z "${RELEASE_TOKEN:-}" ]; then
	notice "RELEASE_TOKEN is not set: Homebrew, apt and rpm publishing skipped"
	exit 0
fi
base=${REPO_BASE:-https://x-access-token:$RELEASE_TOKEN@github.com/moukrea}

clone() { git clone -q --depth 1 "$base/$1" "$work/$1"; }

# publish REPO: commit the changes and push them (unless PUSH=0).
publish() {
	git -C "$work/$1" add -A
	if git -C "$work/$1" diff --cached --quiet; then
		echo "$1: already up to date"
		return
	fi
	git -C "$work/$1" -c user.name="github-actions[bot]" -c user.email="github-actions[bot]@users.noreply.github.com" \
		commit -q -m "automodel $ver"
	if [ "${PUSH:-1}" = 0 ]; then
		git -C "$work/$1" show --stat --format='%s' HEAD
	else
		git -C "$work/$1" push -q origin HEAD
	fi
	echo "$1: automodel $ver published"
}

clone homebrew-tap
mkdir -p "$work/homebrew-tap/Formula"
"$here/homebrew-formula.sh" "$ver" "$dist/checksums.txt" >"$work/homebrew-tap/Formula/automodel.rb"
publish homebrew-tap

if [ -z "${GPG_PRIVATE_KEY:-}" ] || [ -z "${GPG_KEY_ID:-}" ]; then
	notice "GPG_PRIVATE_KEY or GPG_KEY_ID is not set: apt and rpm publishing skipped"
	exit 0
fi
printf '%s\n' "$GPG_PRIVATE_KEY" | gpg --batch --import 2>/dev/null
sign() { gpg --batch --yes --pinentry-mode loopback --default-key "$GPG_KEY_ID" "$@"; }

clone apt-repo
cd "$work/apt-repo"
mkdir -p pool/main/a/automodel
cp "$dist"/automodel_"$ver"_amd64.deb "$dist"/automodel_"$ver"_arm64.deb pool/main/a/automodel/
for arch in amd64 arm64; do
	mkdir -p dists/stable/main/binary-$arch
	dpkg-scanpackages --arch $arch pool/ >dists/stable/main/binary-$arch/Packages 2>/dev/null
	gzip -9kf dists/stable/main/binary-$arch/Packages
done
rm -f dists/stable/Release dists/stable/Release.gpg dists/stable/InRelease
apt-ftparchive \
	-o APT::FTPArchive::Release::Origin=moukrea -o APT::FTPArchive::Release::Label=moukrea \
	-o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
	-o APT::FTPArchive::Release::Architectures="amd64 arm64" -o APT::FTPArchive::Release::Components=main \
	release dists/stable >"$work/Release"
mv "$work/Release" dists/stable/Release
sign -abs -o dists/stable/Release.gpg dists/stable/Release
sign --clearsign -o dists/stable/InRelease dists/stable/Release
cd - >/dev/null
publish apt-repo

clone rpm-repo
cd "$work/rpm-repo"
mkdir -p x86_64 aarch64
cp "$dist"/automodel-"$ver"-*.x86_64.rpm x86_64/
cp "$dist"/automodel-"$ver"-*.aarch64.rpm aarch64/
# Signed packages, so that the repo's gpgcheck=1 holds for them.
rpmsign --define "_gpg_name $GPG_KEY_ID" --define "__gpg $(command -v gpg)" --addsign x86_64/automodel-"$ver"-*.rpm aarch64/automodel-"$ver"-*.rpm >/dev/null
createrepo_c -q --update .
rm -f repodata/repomd.xml.asc
sign --detach-sign --armor repodata/repomd.xml
cd - >/dev/null
publish rpm-repo
