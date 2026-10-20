#!/usr/bin/env bash
# Tests own/autoupdate.sh with a temporary HOME and app, fake releases
# (file:// URLs), a fake magpie binary and a fake gateway on a free local
# port. Nothing here reaches the network, the real app, the real
# LaunchAgents or a real magpie. macOS only; needs go and python3.
#
#   own/test-autoupdate.sh

set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
AU=$HERE/autoupdate.sh
T=$(mktemp -d "${TMPDIR:-/tmp}/own-autoupdate-test.XXXXXX")
T=$(cd "$T" && pwd -P)
PIDS=()
cleanup() {
	local p
	for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	rm -rf "$T"
}
trap cleanup EXIT

# the build cache stays the real one: the fake binary builds in a second
GOCACHE=$(go env GOCACHE)
export GOCACHE GOWORK=off GOFLAGS=
export HOME=$T/home
mkdir -p "$HOME"
unset MAGPIE_ADDR MAGPIE_APP MAGPIE_OWN_REPO MAGPIE_OWN_HOME XDG_CONFIG_HOME
SUPPORT="$HOME/Library/Application Support/magpie-own"
APP=$T/apps/magpie.app
export MAGPIE_APP=$APP
export MAGPIE_OWN_RELEASES_URL=file://$T/releases.json
export MAGPIE_OWN_INSTALL=$T/stub-install.sh
export MAGPIE_OWN_IDLE_SAMPLES=4

PASSED=0
FAILED=0
ok() {
	echo "  ok: $*"
	PASSED=$((PASSED + 1))
}
bad() {
	echo "  FAIL: $*"
	FAILED=$((FAILED + 1))
}
check() {
	local what=$1
	shift
	if "$@"; then ok "$what"; else bad "$what"; fi
}
show() { sed 's/^/    | /' "$@"; }

# fake magpie: --version prints its version; otherwise it sleeps, standing
# in for a running app
mkdir -p "$T/fake"
cat >"$T/fake/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"
	"time"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("magpie " + version)
		return
	}
	time.Sleep(10 * time.Minute)
}
EOF
printf 'module fake\n\ngo 1.21\n' >"$T/fake/go.mod"

# app <path> <version>: an ad-hoc signed magpie.app of that version
app() {
	local a=$1 v=$2
	rm -rf "$a"
	mkdir -p "$a/Contents/MacOS"
	(cd "$T/fake" && GOTOOLCHAIN=local go build -ldflags "-X main.version=$v" -o "$a/Contents/MacOS/magpie" .)
	cat >"$a/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>test.magpie.fake</string>
<key>CFBundleExecutable</key><string>magpie</string>
<key>CFBundleShortVersionString</key><string>$v</string>
</dict></plist>
EOF
	codesign --force -s - "$a" 2>/dev/null
}

# release <version> <id> [created_at]: a zip of that version in
# $T/rel/<id>.zip, printed as a release's JSON
release() {
	local v=$1 id=$2 at=${3:-2026-10-19T10:00:00Z}
	mkdir -p "$T/rel/$id"
	app "$T/rel/$id/magpie.app" "$v"
	(cd "$T/rel/$id" && ditto -c -k --keepParent magpie.app "../$id.zip")
	rm -rf "${T:?}/rel/$id"
	printf '{"tag_name":"own-%s","draft":false,"prerelease":true,"created_at":"%s","assets":[{"name":"magpie-own-cli-darwin-arm64","state":"uploaded","id":1,"browser_download_url":"file:///nonexistent"},{"name":"magpie-own-darwin-arm64.zip","state":"uploaded","id":%s,"digest":"sha256:%s","browser_download_url":"file://%s"}]}' \
		"$v" "$at" "$id" "$(shasum -a 256 "$T/rel/$id.zip" | awk '{print $1}')" "$T/rel/$id.zip"
}

# serve <answer>…: a fake gateway answering each GET with the next
# "<status> <body>" (the last one repeated); PORT is its port, and
# $T/asks counts the asks
cat >"$T/server.py" <<'EOF'
import http.server, sys
answers, asks = sys.argv[1:-1], sys.argv[-1]
n = 0
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        global n
        code, body = answers[min(n, len(answers) - 1)].split(" ", 1)
        n += 1
        open(asks, "w").write("%d %s\n" % (n, self.path))
        self.send_response(int(code))
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body.encode())
    def log_message(self, *a):
        pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
print(s.server_port, flush=True)
s.serve_forever()
EOF
SERVER=
serve() {
	stop
	rm -f "$T/asks" "$T/port"
	python3 -I "$T/server.py" "$@" "$T/asks" >"$T/port" &
	SERVER=$!
	PIDS+=("$SERVER")
	local _
	for _ in $(seq 100); do
		[ -s "$T/port" ] && break
		sleep 0.05
	done
	PORT=$(cat "$T/port")
	export MAGPIE_ADDR=127.0.0.1:$PORT
}
stop() {
	if [ -n "$SERVER" ]; then
		kill "$SERVER" 2>/dev/null || true
		wait "$SERVER" 2>/dev/null || true
		SERVER=
	fi
}
asks() { if [ -f "$T/asks" ]; then awk '{print $1}' "$T/asks"; else echo 0; fi; }

IDLE='200 {"object":"concurrency","data":{},"requests":0,"waiting":0,"tools":0,"active":[]}'
BUSY='200 {"object":"concurrency","data":{},"requests":1,"waiting":0,"tools":0,"active":[{"provider":"x"}]}'

cat >"$T/stub-install.sh" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$T/install.log"
EOF
chmod +x "$T/stub-install.sh"

reset() { # a fresh support folder and install log, the app at version $1
	rm -rf "$SUPPORT" "$T/install.log"
	: >"$T/install.log"
	app "$APP" "$1"
}
# autoupdate.sh runs as the launch agent runs it: /bin/bash, launchd's PATH
LPATH=/usr/bin:/bin:/usr/sbin:/sbin
au() { env PATH="$LPATH" /bin/bash "$AU" "$@" >"$T/out" 2>&1; }
aue() { # aue VAR=value… -- arguments
	local -a e=(PATH="$LPATH")
	while [ "$1" != -- ]; do
		e+=("$1")
		shift
	done
	shift
	env "${e[@]}" /bin/bash "$AU" "$@" >"$T/out" 2>&1
}
installs() { wc -l <"$T/install.log" | tr -d ' '; }
TODAY=$(date +%F)

echo "building the fake app and releases"
R1=$(release v0.1.1294-14-g4561e67d 101 2026-10-19T08:00:00Z)
R2=$(release v0.1.1294-16-g862c1545 102 2026-10-19T09:00:00Z)
# an older build published later must not be taken for newer by number
echo "[$R2,$R1]" >"$T/releases.json"
NEW=v0.1.1294-16-g862c1545
ZIP=$SUPPORT/cache/102.zip

echo "1. the app has the newest build already: nothing to do"
reset "$NEW"
serve "$IDLE"
check "run exits 0" au run
show "$T/out"
check "install.sh not run" [ "$(installs)" = 0 ]
check "nothing downloaded" [ ! -e "$ZIP" ]
check "the gateway not asked" [ "$(asks)" = 0 ]
check "the day not used" [ ! -e "$SUPPORT/state" ]

echo "2. an older app with the newest build being older still: nothing to do"
reset v0.1.1300
check "run exits 0" au run
show "$T/out"
check "install.sh not run" [ "$(installs)" = 0 ]

echo "3. a newer build, magpie idle for the full 30 asks: install.sh gets the pinned zip"
reset v0.1.1294-14-g4561e67d
serve "$IDLE"
START=$(date +%s)
check "run exits 0" aue MAGPIE_OWN_IDLE_SAMPLES=30 -- run
show "$T/out"
check "asked 30 times" [ "$(asks)" = 30 ]
check "over at least 29 seconds" [ $(($(date +%s) - START)) -ge 29 ]
check "asked /v1/magpie/concurrency" grep -q ' /v1/magpie/concurrency$' "$T/asks"
check "install.sh run once with the app and the pinned zip" [ "$(cat "$T/install.log")" = "--app $APP $ZIP" ]
check "the day is used" grep -qx "day=$TODAY" "$SUPPORT/state"

echo "4. a second run the same day, after the install: nothing to do"
check "run exits 0" au run
show "$T/out"
check "install.sh not run again" [ "$(installs)" = 1 ]
check "says so" grep -q 'installed today already' "$T/out"

echo "5. a request shows up in the window: skipped, and the day not used"
reset v0.1.1294-14-g4561e67d
serve "$IDLE" "$IDLE" "$BUSY" "$IDLE"
check "run exits 0" au run
show "$T/out"
check "stopped at the busy answer" [ "$(asks)" = 3 ]
check "install.sh not run" [ "$(installs)" = 0 ]
check "the day not used" [ ! -e "$SUPPORT/state" ]
check "the checked zip kept for the next run" [ -f "$ZIP" ]
serve "$IDLE"
check "the next run (idle) exits 0" au run
show "$T/out"
check "and installs" [ "$(installs)" = 1 ]
check "without downloading again" bash -c "! grep -q downloading '$T/out'"

echo "6. waiting or a tool turn counts as busy"
for answer in '200 {"requests":0,"waiting":1,"tools":0}' '200 {"requests":0,"waiting":0,"tools":2}'; do
	reset v0.1.1294-14-g4561e67d
	serve "$answer"
	au run || true
	check "busy ($answer): install.sh not run" [ "$(installs)" = 0 ]
done

echo "7. an answer that can't be read: skipped"
for answer in '200 not json' '200 {"requests":0,"waiting":0}' '200 {"requests":"0","waiting":0,"tools":0}' '403 {"error":"no"}' '200 '; do
	reset v0.1.1294-14-g4561e67d
	serve "$IDLE" "$answer"
	au run || true
	check "unknown ($answer): install.sh not run" [ "$(installs)" = 0 ]
	check "unknown ($answer): the day not used" [ ! -e "$SUPPORT/state" ]
	check "unknown ($answer): says it can't tell" grep -q "can't tell whether magpie is busy" "$T/out"
done
show "$T/out"

echo "8. nothing listening, but magpie runs from the app: skipped"
reset v0.1.1294-14-g4561e67d
stop
export MAGPIE_ADDR=127.0.0.1:1
"$APP/Contents/MacOS/magpie" &
RUNNING=$!
PIDS+=("$RUNNING")
check "the fake magpie shows in ps as the app's binary" bash -c "ps -axo comm= | grep -qxF '$APP/Contents/MacOS/magpie'"
check "run exits 0" au run
show "$T/out"
check "install.sh not run" [ "$(installs)" = 0 ]
check "the day not used" [ ! -e "$SUPPORT/state" ]
kill "$RUNNING"
wait "$RUNNING" 2>/dev/null || true

echo "9. nothing listening and no magpie running: the real install.sh, without opening the app"
reset v0.1.1294-14-g4561e67d
check "run exits 0" aue MAGPIE_OWN_INSTALL="$HERE/install.sh" -- run
show "$T/out"
check "the app is the new build" [ "$(plutil -extract CFBundleShortVersionString raw -o - "$APP/Contents/Info.plist")" = "$NEW" ]
check "the old one kept as .prev" [ "$(plutil -extract CFBundleShortVersionString raw -o - "$APP.prev/Contents/Info.plist")" = v0.1.1294-14-g4561e67d ]
check "not opened" bash -c "! grep -q 'couldn.t open' '$T/out'"
check "the day is used" grep -qx "day=$TODAY" "$SUPPORT/state"
rm -rf "$APP.prev"

echo "10. the port from magpie's settings.json, MAGPIE_ADDR unset"
reset v0.1.1294-14-g4561e67d
serve "$IDLE"
unset MAGPIE_ADDR
mkdir -p "$HOME/.config/magpie"
echo "{\"theme\":\"dark\",\"port\":$PORT}" >"$HOME/.config/magpie/settings.json"
check "run exits 0" au run
show "$T/out"
check "the gateway on that port was asked" [ "$(asks)" = 4 ]
check "install.sh run" [ "$(installs)" = 1 ]
echo "    and a settings.json that can't be read:"
reset v0.1.1294-14-g4561e67d
echo '{"port": 31' >"$HOME/.config/magpie/settings.json"
if au run; then bad "run succeeded"; else ok "run fails"; fi
show "$T/out"
check "install.sh not run" [ "$(installs)" = 0 ]
rm -rf "$HOME/.config/magpie"

echo "11. a zip whose digest isn't the release's: fails before asking magpie"
reset v0.1.1294-14-g4561e67d
serve "$IDLE"
sed 's/"digest":"sha256:[0-9a-f]*"\(,"browser_download_url":"file:[^"]*102.zip\)/"digest":"sha256:00"\1/' "$T/releases.json" >"$T/bad.json"
if aue MAGPIE_OWN_RELEASES_URL="file://$T/bad.json" -- run; then bad "run succeeded"; else ok "run fails"; fi
show "$T/out"
check "the gateway not asked" [ "$(asks)" = 0 ]
check "install.sh not run" [ "$(installs)" = 0 ]
check "the bad zip not kept" [ ! -e "$ZIP" ]

echo "12. a zip whose app's signature doesn't verify: fails before asking magpie"
reset v0.1.1294-14-g4561e67d
mkdir -p "$T/tamper"
(cd "$T/tamper" && ditto -x -k "$T/rel/102.zip" . && printf x >>magpie.app/Contents/MacOS/magpie && ditto -c -k --keepParent magpie.app "$T/rel/103.zip")
SUM=$(shasum -a 256 "$T/rel/103.zip" | awk '{print $1}')
sed -e "s/\"id\":102,\"digest\":\"sha256:[0-9a-f]*\"/\"id\":103,\"digest\":\"sha256:$SUM\"/" -e "s|$T/rel/102.zip|$T/rel/103.zip|" "$T/releases.json" >"$T/tampered.json"
if aue MAGPIE_OWN_RELEASES_URL="file://$T/tampered.json" -- run; then bad "run succeeded"; else ok "run fails"; fi
show "$T/out"
check "says the signature" grep -q "signature doesn't verify" "$T/out"
check "the gateway not asked" [ "$(asks)" = 0 ]

echo "13. a release list that can't be read: fails, nothing installed"
reset v0.1.1294-14-g4561e67d
echo '<html>rate limited</html>' >"$T/html.json"
if aue MAGPIE_OWN_RELEASES_URL="file://$T/html.json" -- run; then bad "run succeeded"; else ok "run fails"; fi
show "$T/out"
check "install.sh not run" [ "$(installs)" = 0 ]

echo "14. another run holds the lock: this one leaves"
reset v0.1.1294-14-g4561e67d
mkdir -p "$SUPPORT/run.lock"
echo $$ >"$SUPPORT/run.lock/pid"
check "run exits 0" au run
show "$T/out"
check "install.sh not run" [ "$(installs)" = 0 ]
check "the other run's lock left alone" [ -f "$SUPPORT/run.lock/pid" ]
echo 999999 >"$SUPPORT/run.lock/pid"
au run || true
check "a dead run's lock is taken over" grep -q 'taking over' "$T/out"
check "and released" [ ! -e "$SUPPORT/run.lock" ]

echo "15. install and uninstall the launch agent (a stub launchctl)"
stop
rm -rf "$SUPPORT" "$HOME/Library/LaunchAgents"
cat >"$T/launchctl" <<EOF
#!/usr/bin/env bash
echo "launchctl \$*" >>"$T/launchctl.log"
case \$1 in print) [ -e "$T/loaded" ] ;; bootstrap) touch "$T/loaded" ;; bootout) rm -f "$T/loaded" ;; esac
EOF
chmod +x "$T/launchctl"
: >"$T/launchctl.log"
PLIST=$HOME/Library/LaunchAgents/local.magpie-own.autoupdate.plist
check "install exits 0" aue LAUNCHCTL="$T/launchctl" -- install --app "$APP"
show "$T/out"
check "the scripts copied out of the checkout" cmp -s "$AU" "$SUPPORT/autoupdate.sh"
check "install.sh beside them" cmp -s "$HERE/install.sh" "$SUPPORT/install.sh"
check "the plist is valid" plutil -lint "$PLIST"
echo "    the plist:"
show "$PLIST"
check "it runs the copy every 900s" [ "$(plutil -extract StartInterval raw -o - "$PLIST")" = 900 ]
check "with the app" [ "$(plutil -extract EnvironmentVariables.MAGPIE_APP raw -o - "$PLIST")" = "$APP" ]
check "the copy's path, spaces and all" [ "$(plutil -extract ProgramArguments.1 raw -o - "$PLIST")" = "$SUPPORT/autoupdate.sh" ]
check "loaded" grep -q "launchctl bootstrap gui/$(id -u) $PLIST" "$T/launchctl.log"
check "installing again unloads the old one first" aue LAUNCHCTL="$T/launchctl" -- install --app "$APP"
check "bootout before the second bootstrap" grep -q "launchctl bootout gui/$(id -u)/local.magpie-own.autoupdate" "$T/launchctl.log"
check "uninstall exits 0" aue LAUNCHCTL="$T/launchctl" -- uninstall
show "$T/out"
check "unloaded" [ ! -e "$T/loaded" ]
check "the plist gone" [ ! -e "$PLIST" ]
check "the copies gone" bash -c "[ ! -e '$SUPPORT/autoupdate.sh' ] && [ ! -e '$SUPPORT/install.sh' ]"
echo "    launchctl calls:"
show "$T/launchctl.log"

echo
echo "passed $PASSED, failed $FAILED"
[ "$FAILED" -eq 0 ]
