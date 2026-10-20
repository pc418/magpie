#!/usr/bin/env bash
# Keeps the `own` branch on upstream's newest release; run by
# .github/workflows/own-sync.yml, and locally against any two repositories.
#
#   own/sync.sh prepare    decide what to do; for a rebase, leave HEAD at own's
#                          commits rebased onto upstream's newest release
#   own/sync.sh push       push that HEAD to own, leased on the tip prepare
#                          fetched (run the checks first)
#   own/sync.sh dispatch   run own-build.yml for own
#
# prepare's verdict, written to $GITHUB_OUTPUT as action=… (when set) and to
# the state file push and dispatch read:
#   none     own has upstream's newest release, and a build of own's tip is
#            published or under way
#   build    own has upstream's newest release, and no own-* release was built
#            from own's tip: dispatch own-build.yml
#   rebase   HEAD is own's commits rebased onto upstream's newest release
#
# Upstream's newest release is its highest vX.Y.Z release that is published,
# not a draft and not a prerelease, and on upstream main. own is never rebased
# backward: when it has that release already, it is left as it is. Our
# commits are those in own and not in upstream main; a conflict fails the
# run with nothing pushed.
#
# Environment:
#   UPSTREAM        upstream's git URL (default https://github.com/yetone/magpie)
#   UPSTREAM_REPO   upstream's owner/name for the API (default yetone/magpie)
#   FORK            the fork's git URL, fetched from and pushed to
#                   (default https://github.com/$FORK_REPO)
#   FORK_REPO       the fork's owner/name for the API
#                   (default $GITHUB_REPOSITORY, else pc418/magpie)
#   BRANCH          the branch (default own)
#   OWN_SYNC_TOKEN  a token that may push to FORK (contents and workflows),
#                   needed when FORK is https
#   SYNC_STATE      the state file (default own-sync.state in the git dir)
# The API is read through gh (GH_TOKEN).

set -euo pipefail

UPSTREAM=${UPSTREAM:-https://github.com/yetone/magpie}
UPSTREAM_REPO=${UPSTREAM_REPO:-yetone/magpie}
FORK_REPO=${FORK_REPO:-${GITHUB_REPOSITORY:-pc418/magpie}}
FORK=${FORK:-https://github.com/$FORK_REPO}
BRANCH=${BRANCH:-own}
ASSET=magpie-own-darwin-arm64.zip
BUILD_WORKFLOW=own-build.yml
# where prepare keeps what it fetched, apart from the checkout's own refs
FORK_REF=refs/own-sync/fork
UPSTREAM_REF=refs/own-sync/upstream

die() {
	echo "sync.sh: $*" >&2
	exit 1
}

note() {
	echo "sync.sh: $*"
}

usage() {
	sed -n '2,35p' "$0" | sed 's/^# \{0,1\}//'
}

git rev-parse --git-dir >/dev/null 2>&1 || die "run it in a checkout of the fork"
STATE=${SYNC_STATE:-$(git rev-parse --absolute-git-dir)/own-sync.state}

# https needs the token: GITHUB_TOKEN can't push a commit that changes a
# workflow, and its pushes start no workflow (own-build.yml) either.
need_token() {
	case $FORK in
	https://* | http://*)
		[ -n "${OWN_SYNC_TOKEN:-}" ] || die "OWN_SYNC_TOKEN is not set: add a repository secret OWN_SYNC_TOKEN, a token with Contents and Workflows write on $FORK_REPO"
		;;
	esac
}

# verdict records what prepare decided, for push and dispatch and the
# workflow's later steps.
verdict() {
	local action=$1 tip=$2 head=$3 tag=$4
	printf 'action=%s\ntip=%s\nhead=%s\ntag=%s\n' "$action" "$tip" "$head" "$tag" >"$STATE.tmp"
	mv "$STATE.tmp" "$STATE"
	if [ -n "${GITHUB_OUTPUT:-}" ]; then
		printf 'action=%s\ntip=%s\nhead=%s\ntag=%s\n' "$action" "$tip" "$head" "$tag" >>"$GITHUB_OUTPUT"
	fi
	note "action=$action"
}

# state reads one key of the state file; a missing file or key is an error,
# never an empty value.
state() {
	[ -f "$STATE" ] || die "no $STATE: run prepare first"
	local v
	v=$(sed -n "s/^$1=//p" "$STATE")
	[ -n "$v" ] || die "$STATE has no $1"
	printf '%s\n' "$v"
}

# newest_release prints upstream's newest release tag.
newest_release() {
	local json tag
	json=$(gh api "repos/$UPSTREAM_REPO/releases?per_page=100") || die "couldn't list the releases of $UPSTREAM_REPO"
	tag=$(jq -r '
		if type != "array" then error("not a list of releases") else . end
		| [.[] | select(.draft == false and .prerelease == false and .published_at != null)
			| .tag_name | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))]
		| sort_by(ltrimstr("v") | split(".") | map(tonumber))
		| last // ""' <<<"$json") || die "couldn't read the releases of $UPSTREAM_REPO"
	[ -n "$tag" ] || die "$UPSTREAM_REPO has no published release"
	printf '%s\n' "$tag"
}

# built says whether an own-* release with the app was built from commit
# $1: its target is that commit, or its tag (own-<describe>) ends in
# -g<a prefix of it>.
built() {
	local sha=$1 json n
	json=$(gh api "repos/$FORK_REPO/releases?per_page=100") || die "couldn't list the releases of $FORK_REPO"
	n=$(jq -r --arg sha "$sha" --arg asset "$ASSET" '
		if type != "array" then error("not a list of releases") else . end
		| [.[] | select(.draft == false and (.tag_name | startswith("own-")))
			| select(.target_commitish == $sha
				or ((.tag_name | capture("-g(?<g>[0-9a-f]{7,40})$")? // {g: "-"}).g as $g | $sha | startswith($g)))
			| select(any(.assets[]?; .name == $asset and .state == "uploaded"))]
		| length' <<<"$json") || die "couldn't read the releases of $FORK_REPO"
	[ "$n" -gt 0 ]
}

# building says whether a run of own-build.yml for commit $1 hasn't
# finished yet, so a dispatch would only build it twice.
building() {
	local sha=$1 json n
	json=$(gh api "repos/$FORK_REPO/actions/workflows/$BUILD_WORKFLOW/runs?head_sha=$sha&per_page=50") ||
		die "couldn't list the runs of $BUILD_WORKFLOW"
	n=$(jq -r 'if (.workflow_runs | type) != "array" then error("no workflow_runs") else . end
		| [.workflow_runs[] | select(.status != "completed")] | length' <<<"$json") ||
		die "couldn't read the runs of $BUILD_WORKFLOW"
	[ "$n" -gt 0 ]
}

prepare() {
	need_token
	[ -z "$(git status --porcelain --untracked-files=no)" ] || die "the checkout has changes; prepare rebases it"

	git fetch --no-tags "$FORK" "+refs/heads/$BRANCH:$FORK_REF" || die "couldn't fetch $BRANCH from $FORK"
	git fetch --no-tags "$UPSTREAM" "+refs/heads/main:$UPSTREAM_REF" || die "couldn't fetch main from $UPSTREAM"
	local tip tag rel base n
	tip=$(git rev-parse --verify "$FORK_REF^{commit}") || die "no $BRANCH at $FORK"
	note "$BRANCH is at $tip"

	tag=$(newest_release)
	git fetch --no-tags "$UPSTREAM" "+refs/tags/$tag:refs/tags/$tag" || die "couldn't fetch $tag from $UPSTREAM"
	rel=$(git rev-parse --verify "refs/tags/$tag^{commit}") || die "$tag is not a commit"
	git merge-base --is-ancestor "$rel" "$UPSTREAM_REF" || die "$tag ($rel) is not on upstream main"
	note "upstream's newest release is $tag ($rel)"

	if git merge-base --is-ancestor "$rel" "$tip"; then
		note "$BRANCH has $tag already: not rebasing"
		if built "$tip"; then
			note "an own-* release was built from $tip"
			verdict none "$tip" "$tip" "$tag"
		elif building "$tip"; then
			note "no own-* release from $tip yet, but $BUILD_WORKFLOW is building it"
			verdict none "$tip" "$tip" "$tag"
		else
			note "no own-* release was built from $tip"
			verdict build "$tip" "$tip" "$tag"
		fi
		return
	fi

	# Never backward: own's upstream part must be behind the release, so the
	# rebase takes nothing of upstream's away.
	base=$(git merge-base "$tip" "$UPSTREAM_REF") || die "$BRANCH and upstream main share no history"
	git merge-base --is-ancestor "$base" "$rel" ||
		die "$BRANCH's upstream base $base is not behind $tag: not rebasing"
	n=$(git rev-list --count "$UPSTREAM_REF..$tip")
	[ "$n" -gt 0 ] || die "$BRANCH has no commits of its own past upstream main"
	note "rebasing $n commit(s) of $BRANCH onto $tag:"
	git log --oneline "$UPSTREAM_REF..$tip"

	git checkout --quiet --detach "$tip" || die "couldn't check out $tip"
	if ! git rebase --onto "$rel" "$UPSTREAM_REF"; then
		echo "sync.sh: conflicts:" >&2
		git diff --name-only --diff-filter=U >&2 || true
		git rebase --abort || true
		die "rebasing $BRANCH onto $tag conflicts: re-derive our commits on it by hand; nothing was pushed"
	fi
	local head
	head=$(git rev-parse HEAD)
	[ "$head" != "$rel" ] || die "every commit of $BRANCH is in $tag already: nothing of ours to build"
	git merge-base --is-ancestor "$rel" "$head" || die "the rebased $BRANCH doesn't hold $tag"
	note "rebased: $BRANCH would go $tip -> $head"
	git log --oneline "$rel..$head"
	verdict rebase "$tip" "$head" "$tag"
}

push() {
	[ "$(state action)" = rebase ] || die "prepare decided $(state action), not rebase: nothing to push"
	local tip head
	tip=$(state tip)
	head=$(state head)
	[ "$(git rev-parse HEAD)" = "$head" ] || die "HEAD is not the rebased $head prepare left"
	need_token

	local -a auth=(GIT_TERMINAL_PROMPT=0)
	case $FORK in
	https://* | http://*)
		# The push must carry OWN_SYNC_TOKEN alone. A header actions/checkout
		# persisted (GITHUB_TOKEN) would be sent with it.
		local persisted
		# names only: the value is a credential
		persisted=$(git config --name-only --get-regexp '^http\..*extraheader$' | tr '\n' ' ' || true)
		[ -z "$persisted" ] || die "a persisted git credential ($persisted) would go with the push: check out with persist-credentials: false"
		local b64
		b64=$(printf 'x-access-token:%s' "$OWN_SYNC_TOKEN" | base64 | tr -d '\n')
		if [ -n "${GITHUB_ACTIONS:-}" ]; then
			echo "::add-mask::$b64"
		fi
		# through the environment, so the token is in no process's arguments
		auth+=(GIT_CONFIG_COUNT=2
			GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0=
			GIT_CONFIG_KEY_1=http.extraheader "GIT_CONFIG_VALUE_1=AUTHORIZATION: basic $b64")
		;;
	esac

	note "pushing $head to $BRANCH, leased on $tip"
	env "${auth[@]}" git push --force-with-lease="refs/heads/$BRANCH:$tip" "$FORK" "$head:refs/heads/$BRANCH" ||
		die "the push of $head to $BRANCH was refused (did $BRANCH move from $tip since prepare?)"
	note "pushed: $BRANCH is $head"
}

dispatch() {
	[ "$(state action)" = build ] || die "prepare decided $(state action), not build: nothing to dispatch"
	note "running $BUILD_WORKFLOW for $BRANCH ($(state tip))"
	gh workflow run "$BUILD_WORKFLOW" -R "$FORK_REPO" --ref "$BRANCH" || die "couldn't run $BUILD_WORKFLOW"
}

case ${1:-} in
prepare) prepare ;;
push) push ;;
dispatch) dispatch ;;
-h | --help) usage ;;
*) die "usage: sync.sh prepare|push|dispatch (see --help)" ;;
esac
