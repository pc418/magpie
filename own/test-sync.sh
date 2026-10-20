#!/usr/bin/env bash
# Tests own/sync.sh against temporary repositories standing in for upstream
# and the fork, with a stub gh that answers from fixture files. Nothing here
# reaches the network or the real HOME.
#
#   own/test-sync.sh

set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SYNC=$HERE/sync.sh
T=$(mktemp -d "${TMPDIR:-/tmp}/own-sync-test.XXXXXX")
trap 'rm -rf "$T"' EXIT

export HOME=$T/home GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=tester GIT_AUTHOR_EMAIL=tester@example.invalid
export GIT_COMMITTER_NAME=syncer GIT_COMMITTER_EMAIL=syncer@example.invalid
export UPSTREAM_REPO=up/magpie FORK_REPO=fork/magpie
unset GITHUB_OUTPUT GITHUB_ACTIONS OWN_SYNC_TOKEN GH_TOKEN
mkdir -p "$HOME" "$T/bin"

# gh: `api <path>` answers from $FIX/<what>.json; `workflow run` is logged.
cat >"$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"$FIX/gh.log"
case "$1 $2" in
"api repos/up/magpie/releases"*) cat "$FIX/up-releases.json" ;;
"api repos/fork/magpie/releases"*) cat "$FIX/fork-releases.json" ;;
"api repos/fork/magpie/actions/workflows/own-build.yml/runs"*) cat "$FIX/runs.json" ;;
"workflow run") ;;
*) echo "stub gh: unexpected $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$T/bin/gh"
export PATH=$T/bin:$PATH

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
check() { # check <description> <command…>
	local what=$1
	shift
	if "$@"; then ok "$what"; else bad "$what"; fi
}

commit() { # commit <repo> <file> <line> <message>
	printf '%s\n' "$3" >"$1/$2"
	git -C "$1" add "$2"
	git -C "$1" commit -q -m "$4"
}

release() { # release <tag> [draft] [prerelease]
	printf '{"tag_name":"%s","draft":%s,"prerelease":%s,"published_at":%s}' \
		"$1" "${2:-false}" "${3:-false}" "$([ "${2:-false}" = true ] && echo null || echo '"2026-10-19T00:00:00Z"')"
}

# setup <name> [conflict]: upstream main c1 (v0.1.1) c2 c3 (v0.1.2) c4, and a
# fork whose own is c2 plus two commits of ours. With "conflict", c3 changes
# the line our first commit changes.
setup() {
	S=$T/$1
	FIX=$S/fix
	export FIX
	mkdir -p "$S/up" "$FIX"
	: >"$FIX/gh.log"
	git -C "$S/up" init -q -b main
	commit "$S/up" shared.txt base c1
	git -C "$S/up" tag v0.1.1
	commit "$S/up" up.txt two c2
	if [ "${2:-}" = conflict ]; then
		commit "$S/up" shared.txt upstream c3
	else
		commit "$S/up" up.txt three c3
	fi
	git -C "$S/up" tag v0.1.2
	commit "$S/up" up.txt four c4
	# a release tag off main
	git -C "$S/up" checkout -q -b side v0.1.1
	commit "$S/up" side.txt side s1
	git -C "$S/up" tag v0.1.9
	git -C "$S/up" checkout -q main

	git clone -q "$S/up" "$S/seed"
	git -C "$S/seed" checkout -q -b own "$(git -C "$S/up" rev-parse main~2)"
	commit "$S/seed" shared.txt ours "ours: change shared"
	mkdir -p "$S/seed/own"
	commit "$S/seed" own/sync.sh '#ours' "ours: add a script"
	git init -q --bare "$S/fork.git"
	git -C "$S/seed" push -q "$S/fork.git" own

	git clone -q -b own "$S/fork.git" "$S/work"
	export UPSTREAM=$S/up FORK=$S/fork.git SYNC_STATE=$S/state
	printf '[%s,%s,%s]' "$(release v0.1.1)" "$(release v0.1.2)" "$(release v0.1.3 true)" >"$FIX/up-releases.json"
	echo '[]' >"$FIX/fork-releases.json"
	echo '{"total_count":0,"workflow_runs":[]}' >"$FIX/runs.json"
}

forkown() { git -C "$S/fork.git" rev-parse own; }
sync() { (cd "$S/work" && bash "$SYNC" "$@"); }
action() { sed -n 's/^action=//p' "$S/state"; }

echo "1. upstream has a newer release: own is rebased onto it and pushed"
setup new
OLD=$(forkown)
# a prerelease with a higher number is not taken
printf '[%s,%s,%s,%s]' "$(release v0.1.2)" "$(release v0.1.10 false true)" "$(release v0.1.1)" "$(release v0.1.3 true)" >"$FIX/up-releases.json"
export GITHUB_OUTPUT=$S/gh-output
if sync prepare >"$S/out" 2>&1; then ok "prepare succeeds"; else bad "prepare failed"; cat "$S/out"; fi
unset GITHUB_OUTPUT
sed 's/^/    | /' "$S/out"
check "action=rebase" [ "$(action)" = rebase ]
check "action=rebase in GITHUB_OUTPUT" grep -qx 'action=rebase' "$S/gh-output"
HEAD=$(git -C "$S/work" rev-parse HEAD)
check "v0.1.2 is in the rebased HEAD" git -C "$S/work" merge-base --is-ancestor v0.1.2 HEAD
check "c4 (past the release) is not" bash -c "! git -C '$S/work' merge-base --is-ancestor '$(git -C "$S/up" rev-parse main)' HEAD"
check "our two commits sit on v0.1.2" [ "$(git -C "$S/work" rev-list --count v0.1.2..HEAD)" = 2 ]
check "the rebased commits' committer is the sync identity" [ "$(git -C "$S/work" log -1 --format=%cn HEAD)" = syncer ]
check "nothing pushed by prepare" [ "$(forkown)" = "$OLD" ]
if sync push >"$S/out" 2>&1; then ok "push succeeds"; else bad "push failed"; cat "$S/out"; fi
check "the fork's own is the rebased HEAD" [ "$(forkown)" = "$HEAD" ]

echo "2. own already has the newest release, and a build of its tip: nothing to do"
git -C "$S/work" checkout -q -B own "$HEAD"
: >"$FIX/gh.log"
SHORT=$(git -C "$S/work" rev-parse --short=8 HEAD)
printf '[{"tag_name":"own-v0.1.2-2-g%s","draft":false,"prerelease":true,"target_commitish":"own","assets":[{"name":"magpie-own-darwin-arm64.zip","state":"uploaded"}]}]' "$SHORT" >"$FIX/fork-releases.json"
if sync prepare >"$S/out" 2>&1; then ok "prepare succeeds"; else bad "prepare failed"; cat "$S/out"; fi
sed 's/^/    | /' "$S/out"
check "action=none" [ "$(action)" = none ]
check "own not moved" [ "$(forkown)" = "$HEAD" ]
check "HEAD not moved" [ "$(git -C "$S/work" rev-parse HEAD)" = "$HEAD" ]
check "no build dispatched" bash -c "! grep -q 'workflow run' '$FIX/gh.log'"
check "push refuses (nothing to push)" bash -c "! (cd '$S/work' && bash '$SYNC' push) 2>/dev/null"

echo "3. own has the release, but its tip's release has no app (a failed build): build"
printf '[{"tag_name":"own-v0.1.2-2-g%s","draft":false,"prerelease":true,"target_commitish":"%s","assets":[{"name":"magpie-own-cli-darwin-arm64","state":"uploaded"}]},{"tag_name":"own-v0.1.1-9-gdeadbeef","draft":false,"prerelease":true,"target_commitish":"deadbeef","assets":[{"name":"magpie-own-darwin-arm64.zip","state":"uploaded"}]}]' "$SHORT" "$HEAD" >"$FIX/fork-releases.json"
if sync prepare >"$S/out" 2>&1; then ok "prepare succeeds"; else bad "prepare failed"; cat "$S/out"; fi
sed 's/^/    | /' "$S/out"
check "action=build" [ "$(action)" = build ]
if sync dispatch >"$S/out" 2>&1; then ok "dispatch succeeds"; else bad "dispatch failed"; cat "$S/out"; fi
sed 's/^/    | /' "$S/out"
check "gh workflow run own-build.yml for own" grep -qx 'gh workflow run own-build.yml -R fork/magpie --ref own' "$FIX/gh.log"
check "own not moved" [ "$(forkown)" = "$HEAD" ]

echo "4. no build of the tip, but own-build.yml is building it: nothing to do"
echo '[]' >"$FIX/fork-releases.json"
printf '{"total_count":1,"workflow_runs":[{"head_sha":"%s","status":"in_progress"}]}' "$HEAD" >"$FIX/runs.json"
: >"$FIX/gh.log"
if sync prepare >"$S/out" 2>&1; then ok "prepare succeeds"; else bad "prepare failed"; cat "$S/out"; fi
check "action=none" [ "$(action)" = none ]
check "the runs were asked for this sha" grep -q "runs?head_sha=$HEAD" "$FIX/gh.log"
echo '{"total_count":1,"workflow_runs":[{"status":"completed","conclusion":"failure"}]}' >"$FIX/runs.json"
sync prepare >/dev/null 2>&1 || true
check "a finished, failed run: action=build" [ "$(action)" = build ]

echo "5. a conflict: the run fails, nothing is pushed"
setup conflict conflict
OLD=$(forkown)
OLDHEAD=$(git -C "$S/work" rev-parse HEAD)
if sync prepare >"$S/out" 2>&1; then bad "prepare succeeded"; else ok "prepare fails"; fi
sed 's/^/    | /' "$S/out"
check "says it conflicts" grep -q 'conflicts' "$S/out"
check "no rebase left in progress" bash -c "[ ! -d '$S/work/.git/rebase-merge' ] && [ ! -d '$S/work/.git/rebase-apply' ]"
check "the fork's own is unchanged" [ "$(forkown)" = "$OLD" ]
check "the checkout is back at own's tip" [ "$(git -C "$S/work" rev-parse HEAD)" = "$OLDHEAD" ]
check "no verdict to push" bash -c "! (cd '$S/work' && bash '$SYNC' push) 2>/dev/null"

echo "6. the lease: own moved after prepare, so the push is refused"
setup lease
if sync prepare >/dev/null 2>&1; then ok "prepare succeeds"; else bad "prepare failed"; fi
check "action=rebase" [ "$(action)" = rebase ]
git clone -q -b own "$S/fork.git" "$S/other"
commit "$S/other" other.txt moved "someone else's commit"
git -C "$S/other" push -q origin own
MOVED=$(forkown)
if sync push >"$S/out" 2>&1; then bad "push succeeded"; else ok "push fails"; fi
sed 's/^/    | /' "$S/out"
check "the fork's own keeps the other commit" [ "$(forkown)" = "$MOVED" ]

echo "7. a release tag that is not on upstream main: the run fails"
setup offmain
printf '[%s,%s]' "$(release v0.1.2)" "$(release v0.1.9)" >"$FIX/up-releases.json"
if sync prepare >"$S/out" 2>&1; then bad "prepare succeeded"; else ok "prepare fails"; fi
sed 's/^/    | /' "$S/out"
check "says it is not on main" grep -q 'v0.1.9 .* is not on upstream main' "$S/out"

echo "8. an unreadable release list is an error, not 'no release'"
setup unreadable
echo '<html>rate limited</html>' >"$FIX/up-releases.json"
if sync prepare >"$S/out" 2>&1; then bad "prepare succeeded"; else ok "prepare fails"; fi
sed 's/^/    | /' "$S/out"
check "nothing decided" [ ! -f "$S/state" ]

echo "9. an https fork without OWN_SYNC_TOKEN: fails before fetching"
setup token
if (cd "$S/work" && FORK=https://example.invalid/fork bash "$SYNC" prepare) >"$S/out" 2>&1; then bad "prepare succeeded"; else ok "prepare fails"; fi
sed 's/^/    | /' "$S/out"
check "names the secret" grep -q 'OWN_SYNC_TOKEN is not set' "$S/out"
check "nothing asked of gh" [ ! -s "$FIX/gh.log" ]

echo "10. a persisted checkout credential: push refuses to send it with the token"
setup persisted
sync prepare >/dev/null 2>&1 || true
git -C "$S/work" config http.https://example.invalid/.extraheader "AUTHORIZATION: basic Z2l0aHViLXRva2Vu"
if (cd "$S/work" && FORK=https://example.invalid/fork OWN_SYNC_TOKEN=t bash "$SYNC" push) >"$S/out" 2>&1; then bad "push succeeded"; else ok "push fails"; fi
sed 's/^/    | /' "$S/out"
check "says why" grep -q "persist-credentials: false" "$S/out"
check "without the credential itself" bash -c "! grep -q Z2l0aHViLXRva2Vu '$S/out'"

echo "11. the push over https carries OWN_SYNC_TOKEN, and only it"
setup https
sync prepare >/dev/null 2>&1 || true
# a server that writes down each request's Authorization headers, and
# turns every request away
cat >"$S/server.py" <<'PY'
import http.server, sys
log = open(sys.argv[1], "a")
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        log.write("%s %s\n" % (self.path, self.headers.get_all("Authorization")))
        log.flush()
        self.send_response(403)
        self.end_headers()
    do_POST = do_GET
    def log_message(self, *a):
        pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
print(s.server_port, flush=True)
s.serve_forever()
PY
python3 -I "$S/server.py" "$S/auth.log" >"$S/port" &
SERVER=$!
for _ in $(seq 50); do [ -s "$S/port" ] && break; sleep 0.1; done
if (cd "$S/work" && FORK=http://127.0.0.1:$(cat "$S/port")/fork.git OWN_SYNC_TOKEN=tok-123 bash "$SYNC" push) >"$S/out" 2>&1; then bad "push succeeded"; else ok "push fails (the server turns it away)"; fi
kill "$SERVER" 2>/dev/null || true
wait "$SERVER" 2>/dev/null || true
sed 's/^/    | /' "$S/auth.log"
WANT="['basic $(printf 'x-access-token:tok-123' | base64)']"
check "the request carried the token's header alone" grep -qF "$WANT" "$S/auth.log"
check "no request carried another" bash -c "! grep -vF \"$WANT\" '$S/auth.log'"
check "the token is not printed" bash -c "! grep -q tok-123 '$S/out'"

echo
echo "passed $PASSED, failed $FAILED"
[ "$FAILED" -eq 0 ]
