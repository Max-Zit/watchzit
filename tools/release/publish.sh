#!/bin/bash
# Publishes a built release (dist/release-V, see build.sh) on GitHub.
#
#   bash tools/release/publish.sh 1.2.0 notes.md
#   CANDIDATE=1 bash tools/release/publish.sh 1.2.0 notes.md
#
# CANDIDATE=1 publishes a release candidate: a pre-release that consoles on
# the official channel do not see, also copied to the release "testing"
# (the testing channel, update source
# https://github.com/Max-Zit/watchzit/releases/download/testing).
# After the candidate is approved, promote.sh makes it the official release.
set -euo pipefail
V=${1:?version}
NOTES=${2:?release notes file}
REPO=Max-Zit/watchzit
cd "$(dirname "$0")/../.."
R="dist/release-$V"
[ -f "$R/manifest.json.sig" ] || { echo "$R is not a signed release; run build.sh first" >&2; exit 1; }
FLAGS=()
TITLE="WatchZit $V"
if [ -n "${CANDIDATE:-}" ]; then
    FLAGS=(--prerelease --latest=false)
    TITLE="WatchZit $V (release candidate)"
fi
git tag "v$V"
git push -q origin "v$V"
gh release create "v$V" --repo "$REPO" --title "$TITLE" --notes-file "$NOTES" "${FLAGS[@]}" "$R"/*
if [ -n "${CANDIDATE:-}" ]; then
    # The testing channel holds exactly the candidate's signed files.
    if ! gh release view testing --repo "$REPO" >/dev/null 2>&1; then
        gh release create testing --repo "$REPO" --prerelease --latest=false --title "Testing channel" \
            --notes "Release candidates for test consoles. Update channel: Testing (Settings → Updates and server)."
    fi
    for a in $(gh release view testing --repo "$REPO" --json assets --jq '.assets[].name'); do
        gh release delete-asset testing "$a" --repo "$REPO" --yes
    done
    gh release upload testing --repo "$REPO" --clobber "$R"/*
    gh release edit testing --repo "$REPO" --notes "Current candidate: WatchZit $V (https://github.com/$REPO/releases/tag/v$V). For test consoles only: Settings → Updates and server → Update channel: Testing."
    echo "published candidate v$V (also on the testing channel)"
else
    echo "published v$V"
fi
