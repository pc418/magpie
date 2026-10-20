#!/usr/bin/env bash
# Installs a newer build of magpie made by .github/workflows/own-build.yml
# by itself, when magpie is idle, at most once a day (macOS).
#
#   own/autoupdate.sh install [--app PATH]  copy this script and install.sh
#                        to ~/Library/Application Support/magpie-own and load
#                        a launch agent that runs it every 15 minutes
#   own/autoupdate.sh uninstall             unload and remove that agent
#   own/autoupdate.sh run [--app PATH]      one check, what the agent runs
#
# A run, under a lock:
# 1. stops when a run installed a build today (the local day);
# 2. finds the newest own-* release of pc418/magpie (or $MAGPIE_OWN_REPO)
#    that has the app's zip, and stops unless it is newer than the app at
#    PATH (default $MAGPIE_APP, else /Applications/magpie.app);
# 3. downloads that zip and checks it before anything else: its digest, and,
#    unpacked, magpie.app with its binary, a signature that verifies and a
#    --version that is the release's;
# 4. asks magpie's gateway (/v1/magpie/concurrency, at MAGPIE_ADDR, else the
#    port in magpie's settings, else 3425) once a second for 30 seconds.
#    Every answer must say no request, none waiting and no tool turn, or the
#    run stops and the next one tries again; an answer it can't read stops
#    it too. Nothing listening, and no magpie running from the app: it
#    installs without opening the app;
# 5. hands that zip to install.sh, which quits magpie, swaps the app (the
#    old one kept as PATH.prev) and opens it.
# Only an install uses up the day. The log is
# ~/Library/Application Support/magpie-own/autoupdate.log.

set -euo pipefail

REPO=${MAGPIE_OWN_REPO:-pc418/magpie}
ASSET=magpie-own-darwin-arm64.zip
APP=${MAGPIE_APP:-/Applications/magpie.app}
SUPPORT=${MAGPIE_OWN_HOME:-$HOME/Library/Application Support/magpie-own}
LABEL=local.magpie-own.autoupdate
AGENT=$HOME/Library/LaunchAgents/$LABEL.plist
LOG=$SUPPORT/autoupdate.log
LAUNCHCTL=${LAUNCHCTL:-launchctl}
RELEASES=${MAGPIE_OWN_RELEASES_URL:-https://api.github.com/repos/$REPO/releases?per_page=100}
INSTALL=${MAGPIE_OWN_INSTALL:-$(cd "$(dirname "$0")" && pwd)/install.sh}
# the idle window: this many answers, a second apart
SAMPLES=${MAGPIE_OWN_IDLE_SAMPLES:-30}
DEFAULT_PORT=3425
BIN=Contents/MacOS/magpie
INTERVAL=900

die() {
	echo "$(date '+%F %T') autoupdate.sh: $*" >&2
	exit 1
}

log() {
	echo "$(date '+%F %T') $*"
}

usage() {
	sed -n '2,31p' "$0" | sed 's/^# \{0,1\}//'
}

MODE=${1:-}
[ $# -eq 0 ] || shift
APP_SET=false
while [ $# -gt 0 ]; do
	case $1 in
	--app)
		[ $# -ge 2 ] || die "--app needs a path"
		APP=$2
		APP_SET=true
		shift 2
		;;
	--app=*)
		APP=${1#--app=}
		APP_SET=true
		shift
		;;
	*) die "unknown argument $1 (see --help)" ;;
	esac
done
APP=${APP%/}
case $APP in
/*.app) ;;
*) die "$APP: the app path must be absolute and end in .app" ;;
esac

# xml escapes a value for the plist.
xml() {
	local s=$1
	s=${s//&/&amp;}
	s=${s//</&lt;}
	s=${s//>/&gt;}
	printf '%s' "$s"
}

# loaded says whether the agent is loaded in this user's session.
loaded() {
	"$LAUNCHCTL" print "gui/$(id -u)/$LABEL" >/dev/null 2>&1
}

install_agent() {
	local here
	here=$(cd "$(dirname "$0")" && pwd)
	[ -f "$here/install.sh" ] || die "no install.sh beside $0"
	mkdir -p "$SUPPORT" "$(dirname "$AGENT")" || die "couldn't make $SUPPORT"
	# copies outside any checkout, so a checkout's branch or state never
	# changes what the agent runs
	local f
	for f in autoupdate.sh install.sh; do
		cp "$here/$f" "$SUPPORT/.$f.new" || die "couldn't copy $f to $SUPPORT"
		chmod 755 "$SUPPORT/.$f.new"
		mv "$SUPPORT/.$f.new" "$SUPPORT/$f" || die "couldn't put $f in $SUPPORT"
	done

	local env=""
	env+="		<key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin</string>"$'\n'
	if $APP_SET || [ -n "${MAGPIE_APP:-}" ]; then
		env+="		<key>MAGPIE_APP</key><string>$(xml "$APP")</string>"$'\n'
	fi
	if [ -n "${MAGPIE_OWN_REPO:-}" ]; then
		env+="		<key>MAGPIE_OWN_REPO</key><string>$(xml "$MAGPIE_OWN_REPO")</string>"$'\n'
	fi
	cat >"$AGENT.new" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/bash</string>
		<string>$(xml "$SUPPORT/autoupdate.sh")</string>
		<string>run</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
$env	</dict>
	<key>StartInterval</key><integer>$INTERVAL</integer>
	<key>RunAtLoad</key><true/>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>$(xml "$LOG")</string>
	<key>StandardErrorPath</key><string>$(xml "$LOG")</string>
</dict>
</plist>
EOF
	plutil -lint "$AGENT.new" >/dev/null || die "the launch agent written isn't a valid plist ($AGENT.new)"
	mv "$AGENT.new" "$AGENT" || die "couldn't write $AGENT"
	if loaded; then
		"$LAUNCHCTL" bootout "gui/$(id -u)/$LABEL" || die "couldn't unload the agent loaded before"
	fi
	"$LAUNCHCTL" bootstrap "gui/$(id -u)" "$AGENT" || die "couldn't load $AGENT"
	echo "Installed: $LABEL runs $SUPPORT/autoupdate.sh every $((INTERVAL / 60)) minutes for $APP"
	echo "Log: $LOG"
}

uninstall_agent() {
	if loaded; then
		"$LAUNCHCTL" bootout "gui/$(id -u)/$LABEL" || die "couldn't unload $LABEL"
	fi
	rm -f "$AGENT" || die "couldn't remove $AGENT"
	rm -rf "$SUPPORT/autoupdate.sh" "$SUPPORT/install.sh" "$SUPPORT/cache" "$SUPPORT/run.lock" ||
		die "couldn't remove the copies in $SUPPORT"
	echo "Uninstalled $LABEL. The log and the day's record stay in $SUPPORT."
}

# --- run ---

WORK=
LOCKED=false
LOCK=$SUPPORT/run.lock
STATE=$SUPPORT/state

cleanup() {
	[ -z "$WORK" ] || rm -rf "$WORK"
	if $LOCKED; then rm -rf "$LOCK"; fi
}

# lock takes the run lock, or exits when another run holds it. A lock whose
# run is gone (its pid dead, or no pid after a minute) is taken over.
lock() {
	mkdir -p "$SUPPORT" || die "couldn't make $SUPPORT"
	local try pid
	for try in 1 2; do
		if mkdir "$LOCK" 2>/dev/null; then
			LOCKED=true
			echo $$ >"$LOCK/pid" || die "couldn't write $LOCK/pid"
			return 0
		fi
		[ "$try" = 1 ] || break
		pid=$(cat "$LOCK/pid" 2>/dev/null || true)
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			log "another run (pid $pid) holds $LOCK"
			exit 0
		fi
		if [ -z "$pid" ] && [ -z "$(find "$LOCK" -maxdepth 0 -mmin +1 2>/dev/null)" ]; then
			log "another run is taking $LOCK"
			exit 0
		fi
		log "taking over $LOCK from a run that is gone (pid ${pid:-none})"
		rm -rf "$LOCK" || die "couldn't remove the stale $LOCK"
	done
	log "another run took $LOCK"
	exit 0
}

# state_day is the day the last install was made, "" when none was ever
# recorded; a state file that can't be read is an error.
state_day() {
	[ -e "$STATE" ] || return 0
	[ -r "$STATE" ] || die "can't read $STATE"
	sed -n 's/^day=//p' "$STATE" || die "can't read $STATE"
}

record_install() {
	printf 'day=%s\nversion=%s\n' "$1" "$2" >"$STATE.new" || die "couldn't write $STATE.new"
	mv "$STATE.new" "$STATE" || die "couldn't write $STATE"
}

# key turns a version (v0.1.1294, v0.1.1294-16-g862c1545, own-v…) into its
# numbers, space-separated ("0 1 1294 16"); fails for anything else.
key() {
	local v=$1
	v=${v#own-}
	v=${v#v}
	v=${v%-dirty}
	if [[ $v =~ ^(.*)-g[0-9a-f]+$ ]]; then v=${BASH_REMATCH[1]}; fi
	[[ $v =~ ^[0-9]+(\.[0-9]+)*(-[0-9]+)?$ ]] || return 1
	printf '%s\n' "${v//[.-]/ }"
}

# newer says whether version $1 is newer than $2: higher numbers, or the
# same numbers and another build (the newest release is the newest made).
newer() {
	local a b i
	read -ra a <<<"$(key "$1")" || return 2
	read -ra b <<<"$(key "$2")" || return 2
	[ ${#a[@]} -gt 0 ] && [ ${#b[@]} -gt 0 ] || return 2
	for ((i = 0; i < ${#a[@]} || i < ${#b[@]}; i++)); do
		local x=$((10#${a[i]:-0})) y=$((10#${b[i]:-0}))
		[ "$x" -eq "$y" ] || { [ "$x" -gt "$y" ]; return; }
	done
	[ "${1#own-}" != "$2" ]
}

# addr is where magpie's gateway listens, as magpie works it out
# (settings.GatewayAddr): MAGPIE_ADDR, else the port in settings.json in
# magpie's folder (appdir.Config), else 3425, on 127.0.0.1.
addr() {
	if [ -n "${MAGPIE_ADDR:-}" ]; then
		local host=${MAGPIE_ADDR%:*} port=${MAGPIE_ADDR##*:}
		[[ $port =~ ^[0-9]+$ ]] || die "MAGPIE_ADDR=$MAGPIE_ADDR has no port"
		case $host in
		"" | 0.0.0.0 | "[::]") host=127.0.0.1 ;;
		esac
		printf '%s:%s\n' "$host" "$port"
		return
	fi
	local file port
	file=$(config_dir)/settings.json
	port=0
	if [ -e "$file" ]; then
		port=$(jq -r 'if type != "object" then error("not an object") else (.port // 0) end
			| if type == "number" and . == floor then . else error("port is not a whole number") end' "$file") ||
			die "can't read the port in $file"
	fi
	# 0 or one magpie won't listen on means the default (settings.SavedAddr)
	if [ "$port" -lt 1024 ] || [ "$port" -gt 65535 ]; then
		port=$DEFAULT_PORT
	fi
	printf '127.0.0.1:%s\n' "$port"
}

# config_dir is the folder magpie run from $APP keeps its settings in:
# portable (a "data" folder beside the app, appdir.Resolve), else
# $XDG_CONFIG_HOME/magpie, else ~/.config/magpie.
config_dir() {
	local exe base data installed
	exe=$(realpath "$APP/$BIN" 2>/dev/null || printf '%s' "$APP/$BIN")
	base=$(dirname "$exe")
	if [ "$(basename "$base")" = MacOS ] && [ "$(basename "$(dirname "$base")")" = Contents ]; then
		base=$(dirname "$(dirname "$(dirname "$base")")")
	fi
	data=$base/data
	if [[ ${XDG_CONFIG_HOME:-} == /* ]]; then
		installed=$XDG_CONFIG_HOME/magpie
	else
		installed=$HOME/.config/magpie
	fi
	if [ -f "$base/.portable" ]; then
		printf '%s\n' "$data"
		return
	fi
	if [ -d "$data" ]; then
		# a data folder in a folder every app shares is magpie's only when it
		# holds magpie's files and the installed folder doesn't (appdir.shared)
		if ! shared_folder "$base" || { has_files "$data" && ! has_files "$installed"; }; then
			printf '%s\n' "$data"
			return
		fi
	fi
	printf '%s\n' "$installed"
}

shared_folder() {
	local dir d
	dir=$(printf '%s' "${1%/}" | tr '[:upper:]' '[:lower:]')
	for d in /Applications "$HOME" "$HOME/Downloads" "$HOME/Desktop" "$HOME/Documents" "$HOME/Applications"; do
		[ "$dir" != "$(printf '%s' "${d%/}" | tr '[:upper:]' '[:lower:]')" ] || return 0
	done
	return 1
}

has_files() {
	local f
	for f in providers.json logins.json settings.json; do
		[ -f "$1/$f" ] && return 0
	done
	return 1
}

# running says whether a magpie runs from $APP's binary; ps failing is an
# error, never "not running".
running() {
	local exe=$APP/$BIN out pid comm
	out=$(ps -axo pid=,comm=) || die "couldn't list processes"
	while read -r pid comm; do
		[ "$comm" = "$exe" ] && return 0
	done <<<"$out"
	return 1
}

# idle prints idle, busy, stopped or unknown: what SAMPLES answers of the
# gateway, a second apart, say. Nothing listening at the first ask is
# stopped; anything else that isn't an answer with every count 0 is busy
# (a count above 0) or unknown.
idle() {
	local url="http://$1/v1/magpie/concurrency" i rc code counts
	for ((i = 1; i <= SAMPLES; i++)); do
		[ "$i" = 1 ] || sleep 1
		rc=0
		code=$(curl -sS --noproxy '*' --max-time 3 -o "$WORK/concurrency.json" -w '%{http_code}' "$url" 2>"$WORK/curl.err") || rc=$?
		if [ "$rc" = 7 ] && [ "$i" = 1 ]; then
			log "nothing listens at $1" >&2
			echo stopped
			return
		fi
		if [ "$rc" != 0 ]; then
			log "ask $i of $url failed: $(tr '\n' ' ' <"$WORK/curl.err")" >&2
			echo unknown
			return
		fi
		if [ "$code" != 200 ]; then
			log "ask $i of $url answered $code" >&2
			echo unknown
			return
		fi
		if ! counts=$(jq -r 'if (.requests | type) == "number" and (.waiting | type) == "number" and (.tools | type) == "number"
			then "\(.requests) \(.waiting) \(.tools)" else error("no counts") end' "$WORK/concurrency.json" 2>/dev/null) ||
			! [[ $counts =~ ^[0-9]+\ [0-9]+\ [0-9]+$ ]]; then
			log "ask $i of $url: an answer without the counts: $(head -c 200 "$WORK/concurrency.json")" >&2
			echo unknown
			return
		fi
		if [ "$counts" != "0 0 0" ]; then
			log "ask $i: busy (requests, waiting, tools: $counts)" >&2
			echo busy
			return
		fi
	done
	echo idle
}

# check_zip checks the app in zip $1 is release $2's, as install.sh does.
check_zip() {
	local zip=$1 want=$2 dir=$WORK/check got
	rm -rf "$dir"
	mkdir "$dir"
	ditto -x -k "$zip" "$dir" || die "$zip: couldn't unpack"
	[ -x "$dir/magpie.app/$BIN" ] || die "$zip holds no magpie.app with $BIN"
	xattr -dr com.apple.quarantine "$dir/magpie.app" 2>/dev/null || true
	codesign --verify --strict "$dir/magpie.app" || die "$zip: magpie.app's signature doesn't verify"
	got=$(HOME="$WORK" "$dir/magpie.app/$BIN" --version) || die "$zip: magpie --version failed"
	[ "$got" = "magpie $want" ] || die "$zip: magpie --version printed '$got', want 'magpie $want'"
	rm -rf "$dir"
}

run() {
	[ "$(uname -s)" = Darwin ] || die "macOS only"
	command -v jq >/dev/null || die "jq not found (macOS 15 and later have /usr/bin/jq)"
	trap cleanup EXIT
	lock
	# the log launchd appends to is cut at 1 MB
	if [ -f "$LOG" ] && [ "$(stat -f %z "$LOG")" -gt 1048576 ]; then
		mv -f "$LOG" "$LOG.old" || true
	fi

	local today last
	today=$(date +%F)
	last=$(state_day)
	if [ "$last" = "$today" ]; then
		log "a build was installed today already"
		return 0
	fi

	[ -d "$APP" ] || die "no app at $APP: install one with install.sh first"
	local have
	have=$(plutil -extract CFBundleShortVersionString raw -o - "$APP/Contents/Info.plist") ||
		die "can't read the version of $APP"

	WORK=$(mktemp -d "${TMPDIR:-/tmp}/magpie-autoupdate.XXXXXX")
	curl -fsSL --max-time 60 -H 'Accept: application/vnd.github+json' -o "$WORK/releases.json" "$RELEASES" ||
		die "couldn't list the releases of $REPO"
	local pick tag url digest id
	pick=$(jq -r --arg asset "$ASSET" '
		if type != "array" then error("not a list of releases") else . end
		| [.[] | select(.draft == false and (.tag_name | startswith("own-")))
			| . as $r | (.assets[]? | select(.name == $asset and .state == "uploaded")) as $a
			| {created_at: $r.created_at, tag: $r.tag_name, url: $a.browser_download_url, digest: ($a.digest // ""), id: $a.id}]
		| sort_by(.created_at) | last
		| if . == null then "" else "\(.tag)\t\(.url)\t\(.digest)\t\(.id)" end' "$WORK/releases.json") ||
		die "couldn't read the releases of $REPO"
	if [ -z "$pick" ]; then
		log "$REPO has no own-* release with $ASSET"
		return 0
	fi
	IFS=$'\t' read -r tag url digest id <<<"$pick"
	[[ $id =~ ^[0-9]+$ ]] || die "$tag: $ASSET has no id"
	local want=${tag#own-} rc=0
	newer "$tag" "$have" || rc=$?
	case $rc in
	0) ;;
	1)
		log "the newest build is $want; $APP has $have"
		return 0
		;;
	*) die "can't compare $want with $APP's version $have" ;;
	esac
	log "$tag is newer than $have"

	# Pin this release's zip: the same file is checked and installed. It is
	# kept until a newer one, so a busy magpie doesn't mean a download every
	# run.
	local cache=$SUPPORT/cache zip
	zip=$cache/$id.zip
	mkdir -p "$cache" || die "couldn't make $cache"
	if [ ! -f "$zip" ]; then
		log "downloading $url"
		curl -fsSL --max-time 900 -o "$WORK/$ASSET" "$url" || die "couldn't download $url"
		mv "$WORK/$ASSET" "$zip" || die "couldn't keep $zip"
	fi
	find "$cache" -type f ! -name "$id.zip" -delete 2>/dev/null || true
	if [ -n "$digest" ]; then
		local sum
		sum=$(shasum -a 256 "$zip" | awk '{print $1}') || die "couldn't hash $zip"
		if [ "sha256:$sum" != "$digest" ]; then
			rm -f "$zip"
			die "$zip: sha256 $sum, but the release says $digest"
		fi
	else
		log "the release gives no digest for $ASSET; checking the app alone"
	fi
	check_zip "$zip" "$want"
	log "checked $tag's $ASSET"

	local where state
	where=$(addr)
	state=$(idle "$where")
	local -a opts=(--app "$APP")
	case $state in
	idle) log "magpie at $where was idle for ${SAMPLES}s" ;;
	busy)
		log "magpie is busy: trying again next run"
		return 0
		;;
	stopped)
		if running; then
			log "magpie runs from $APP but doesn't answer at $where: trying again next run"
			return 0
		fi
		log "magpie isn't running: installing without opening it"
		opts+=(--no-launch)
		;;
	*)
		log "can't tell whether magpie is busy: trying again next run"
		return 0
		;;
	esac

	bash "$INSTALL" "${opts[@]}" "$zip" || die "install.sh failed for $tag; trying again next run"
	record_install "$today" "$want"
	rm -f "$zip"
	log "installed $want"
}

case $MODE in
install) install_agent ;;
uninstall) uninstall_agent ;;
run) run ;;
-h | --help) usage ;;
*) die "usage: autoupdate.sh install|uninstall|run [--app PATH] (see --help)" ;;
esac
