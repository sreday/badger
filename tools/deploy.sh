#!/usr/bin/env bash
# Deploy Snout (and the tools it hosts) to scan.sreday.com.
#
# The only way to deploy. It refuses to ship anything that could take a
# feature off the live site:
#   1. the working tree must be clean (nothing uncommitted, no stray .go/.html),
#   2. HEAD must be origin/main, pushed (what is live is always on GitHub),
#   3. the build that is live now must be part of HEAD's history. If it is
#      not, another machine deployed work it never pushed: installing this
#      build would silently remove that work. Push it from that machine,
#      pull here, deploy again.
#
# Usage: tools/deploy.sh            deploy origin/main
#        tools/deploy.sh --check    run the checks, deploy nothing
#        tools/deploy.sh --replace <sha>
#                                   deploy although live build <sha> is not in
#                                   this history (only after checking by hand
#                                   that nothing in it is missing from main)
set -euo pipefail

VM="${SNOUT_VM:-badger-scan}"
ZONE="${SNOUT_ZONE:-us-central1-a}"
PROJECT="${SNOUT_PROJECT:-sreday-badger-scan}"
DIR=/opt/badger-scan

check_only=0
replace=""
while [ $# -gt 0 ]; do
	case "$1" in
	--check) check_only=1 ;;
	--replace) replace="${2:?--replace needs the sha of the live build}"; shift ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
	shift
done

stop() { echo "NOT DEPLOYED: $*" >&2; exit 1; }
remote() { gcloud compute ssh "$VM" --zone "$ZONE" --project "$PROJECT" --command "$1"; }

cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
export PATH="$PATH:/usr/local/go/bin"

# 1. Clean tree.
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || stop "not on main."
dirty="$(git status --porcelain)"
[ -z "$dirty" ] || stop "uncommitted or untracked files. Commit and push them first:
$dirty"

# 2. Pushed.
git fetch -q origin main
head="$(git rev-parse HEAD)"
[ "$head" = "$(git rev-parse origin/main)" ] ||
	stop "HEAD is not origin/main. Pull (git pull --rebase) and push first."

# 3. The live build is in this history.
live="$(remote "cat $DIR/VERSION 2>/dev/null || sudo journalctl -u badger-scan -n 2000 --no-pager | grep -o 'snout v[^ ]* starting' | tail -1" | tr -d '\r' | tail -1)"
live_sha="$(printf '%s' "$live" | grep -oE 'g[0-9a-f]{7,40}' | tail -1 | cut -c2- || true)"
case "$live" in *-dirty*) live_dirty=1 ;; *) live_dirty=0 ;; esac
echo "live now: ${live:-unknown}"
if [ -z "$live_sha" ]; then
	[ -n "$replace" ] || stop "can't tell which build is live. Find out, then --replace <sha>."
elif [ -n "$replace" ] && [[ "$live_sha" == "$replace"* || "$replace" == "$live_sha"* ]]; then
	echo "replacing $live_sha as asked (--replace)"
elif [ "$live_dirty" = 1 ]; then
	stop "the live build ($live) was made from uncommitted files on some machine.
Commit and push that work there, pull here, then deploy."
elif ! git cat-file -e "$live_sha^{commit}" 2>/dev/null; then
	stop "the live build ($live_sha) is a commit that was never pushed: another
machine deployed it. Deploying now would remove that work from the site.
Push it from that machine, pull here, then deploy."
elif ! git merge-base --is-ancestor "$live_sha" "$head"; then
	stop "the live build ($live_sha) is not part of main's history.
Deploying now could remove what it has. Merge it into main first."
fi

version="$(git describe --tags --always)"
echo "checks passed: $version can replace the live build"
[ "$check_only" = 0 ] || exit 0

# Build and install.
out="$(mktemp -d)/snout"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-X main.version=$version" -o "$out" ./snout
sum="$(shasum -a 256 "$out" 2>/dev/null || sha256sum "$out")"
sum="${sum%% *}"
gcloud compute scp "$out" "$VM:snout-new" --zone "$ZONE" --project "$PROJECT"
remote "set -e
echo '$sum  snout-new' | sha256sum -c -
sudo cp $DIR/badger-scan $DIR/badger-scan.prev
sudo install -m 755 ~/snout-new $DIR/badger-scan
echo 'snout $version starting' | sudo tee $DIR/VERSION >/dev/null
sudo systemctl restart badger-scan
sleep 2
systemctl is-active badger-scan
curl -fsS -o /dev/null http://127.0.0.1:8081/robots.txt
echo deployed $version"
