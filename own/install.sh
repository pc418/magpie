#!/usr/bin/env bash
# Installs a build of magpie made by .github/workflows/own-build.yml (macOS).
#
#   own/install.sh                  the newest own-* release of pc418/magpie
#                                   (or of $MAGPIE_OWN_REPO)
#   own/install.sh path/to/app.zip  a zip of magpie.app on disk
#   own/install.sh --rollback       swap magpie.app.prev back in
#
# Options:
#   --app PATH    the app to replace (default $MAGPIE_APP, else
#                 /Applications/magpie.app); its previous copy is PATH.prev
#   --no-launch   don't open the app afterwards
#
# The magpie running from that path is asked to quit (SIGTERM, which it
# handles as a normal quit) and waited for. Every step fails loudly, and the
# app path is never left empty: if the new app can't be moved in, the old
# one goes back.

set -euo pipefail

REPO=${MAGPIE_OWN_REPO:-pc418/magpie}
ASSET=magpie-own-darwin-arm64.zip
APP=${MAGPIE_APP:-/Applications/magpie.app}
ZIP=
ROLLBACK=false
LAUNCH=true
QUIT_WAIT=${MAGPIE_QUIT_WAIT:-30}

die() {
	echo "install.sh: $*" >&2
	exit 1
}

usage() {
	sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
	case $1 in
	--app)
		[ $# -ge 2 ] || die "--app needs a path"
		APP=$2
		shift 2
		;;
	--app=*)
		APP=${1#--app=}
		shift
		;;
	--rollback)
		ROLLBACK=true
		shift
		;;
	--no-launch)
		LAUNCH=false
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	-*) die "unknown option $1 (see --help)" ;;
	*)
		[ -z "$ZIP" ] || die "one zip at a time"
		ZIP=$1
		shift
		;;
	esac
done

[ "$(uname -s)" = Darwin ] || die "macOS only"
APP=${APP%/}
[ -n "$APP" ] || die "empty app path"
case $APP in
*.app) ;;
*) die "$APP: the app path must end in .app" ;;
esac
PREV=$APP.prev
BIN=Contents/MacOS/magpie

WORK=$(mktemp -d "${TMPDIR:-/tmp}/magpie-install.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

# quit ends every magpie running from $APP's binary and waits for it to go.
quit() {
	local exe=$APP/$BIN pids="" pid comm i
	while read -r pid comm; do
		[ "$comm" = "$exe" ] && pids="$pids $pid"
	done < <(ps -axo pid=,comm=)
	[ -n "$pids" ] || return 0
	echo "Quitting magpie ($exe, pid$pids)"
	# shellcheck disable=SC2086 # one pid per word
	kill -TERM $pids || die "couldn't signal magpie (pid$pids)"
	for ((i = 0; i < QUIT_WAIT * 10; i++)); do
		local alive=false
		for pid in $pids; do
			kill -0 "$pid" 2>/dev/null && alive=true
		done
		$alive || return 0
		sleep 0.1
	done
	die "magpie (pid$pids) is still running after ${QUIT_WAIT}s; quit it and run again"
}

launch() {
	if $LAUNCH; then
		open "$APP" || die "installed, but couldn't open $APP"
	fi
}

if $ROLLBACK; then
	[ -z "$ZIP" ] || die "--rollback takes no zip"
	[ -d "$PREV" ] || die "no $PREV to roll back to"
	quit
	# swapped, so rolling back again goes forward again; the newer app waits
	# beside them, not in $WORK, which goes when the script ends
	SIDE=$APP.rollback-$$
	if [ -e "$APP" ]; then
		mv "$APP" "$SIDE" || die "couldn't move $APP aside"
	fi
	if ! mv "$PREV" "$APP"; then
		if [ -e "$SIDE" ]; then
			mv "$SIDE" "$APP" || die "couldn't move $PREV in, nor $APP back: it is at $SIDE"
		fi
		die "couldn't move $PREV to $APP; $APP is unchanged"
	fi
	if [ -e "$SIDE" ]; then
		mv "$SIDE" "$PREV" || die "rolled back, but the newer app is left at $SIDE"
	fi
	echo "Rolled back: $APP is $(HOME="$WORK" "$APP/$BIN" --version)"
	launch
	exit 0
fi

if [ -z "$ZIP" ]; then
	command -v gh >/dev/null || die "gh not found; install it or pass a zip"
	TAG=$(gh release list -R "$REPO" --limit 200 --json tagName,createdAt \
		--jq '[.[] | select(.tagName | startswith("own-"))] | sort_by(.createdAt) | last | .tagName // ""') ||
		die "couldn't list the releases of $REPO"
	[ -n "$TAG" ] || die "$REPO has no own-* release"
	echo "Downloading $ASSET from $REPO $TAG"
	gh release download "$TAG" -R "$REPO" -p "$ASSET" -D "$WORK" || die "couldn't download $ASSET of $TAG"
	ZIP=$WORK/$ASSET
fi
[ -f "$ZIP" ] || die "$ZIP: no such file"

# Unpack and check the new app before anything is touched.
mkdir "$WORK/new"
ditto -x -k "$ZIP" "$WORK/new" || die "$ZIP: couldn't unpack"
NEW=$WORK/new/magpie.app
[ -x "$NEW/$BIN" ] || die "$ZIP holds no magpie.app with $BIN"
xattr -dr com.apple.quarantine "$NEW" 2>/dev/null || true
codesign --verify --strict "$NEW" || die "$ZIP: magpie.app's signature doesn't verify"
VERSION=$(HOME="$WORK" "$NEW/$BIN" --version) || die "$ZIP: magpie --version failed"
case $VERSION in
"magpie "?*) ;;
*) die "$ZIP: magpie --version printed '$VERSION'" ;;
esac
echo "New: $VERSION"

quit
# The older .prev waits beside it until the new app is in, so a failed
# install leaves both as they were.
OLDER=$PREV.older-$$
KEPT=false
if [ -e "$APP" ]; then
	if [ -e "$PREV" ]; then
		mv "$PREV" "$OLDER" || die "couldn't move the older $PREV aside"
	fi
	if ! mv "$APP" "$PREV"; then
		[ ! -e "$OLDER" ] || mv "$OLDER" "$PREV" || echo "install.sh: the older $PREV is left at $OLDER" >&2
		die "couldn't move $APP to $PREV; $APP is unchanged"
	fi
	KEPT=true
fi
if ! mv "$NEW" "$APP"; then
	# a move across volumes copies, and can leave half an app behind
	rm -rf "$APP"
	if $KEPT; then
		mv "$PREV" "$APP" || die "couldn't move the new app in, nor $PREV back"
		[ ! -e "$OLDER" ] || mv "$OLDER" "$PREV" || echo "install.sh: the older $PREV is left at $OLDER" >&2
		die "couldn't move the new app to $APP; the previous one is back"
	fi
	die "couldn't move the new app to $APP"
fi
rm -rf "$OLDER" || echo "install.sh: couldn't remove $OLDER" >&2
# a quarantine flag on the app itself would hold the first launch
xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true
if [ -n "$(xattr -r "$APP" 2>/dev/null | grep com.apple.quarantine || true)" ]; then
	die "installed, but couldn't remove the quarantine flag from $APP"
fi

if $KEPT; then
	echo "Installed $VERSION at $APP (previous at $PREV)"
else
	echo "Installed $VERSION at $APP"
fi
launch
