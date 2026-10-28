#!/bin/bash
# Builds a release into dist/: agent MSI + exe + Linux agents, the launcher,
# the server for Linux (deb, rpm, plain program) and Windows, third-party
# licenses and, with the release key, the signed manifest of dist/release-V
# (the files that go on GitHub).
#   bash tools/release/build.sh 0.9.0 [release-notes.md]
set -euo pipefail
V=${1:?version}
NOTES=${2:-}
KEY=${WATCHZIT_RELEASE_KEY:-/c/Users/Mili/Ai/release-keys/watchzit-release.key}
cd "$(dirname "$0")/../.."
export GOTMPDIR=$PWD/.gotmp CGO_ENABLED=0
mkdir -p dist
powershell -NoProfile -ExecutionPolicy Bypass -File packaging/windows/build-msi.ps1 -Version "$V" | tail -1
cp bin/msi/watchzit-agent.exe "dist/watchzit-agent-$V-windows-amd64.exe"
for arch in amd64 arm64; do
  GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$V" -o "dist/watchzit-agent-$V-linux-$arch" ./cmd/watchzit-agent
done
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$V" -o "dist/watchzit-launcher-$V-windows-amd64.exe" ./cmd/watchzit-launcher
for os in linux windows; do
  ext=; [ $os = windows ] && ext=.exe
  GOOS=$os GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$V" -o "dist/watchzit-server-$V-$os-amd64$ext" ./cmd/watchzit-server
done
# Licenses of everything linked into the programs (fails on an unknown license).
go run ./tools/licenses -out THIRD_PARTY_LICENSES.md "dist/watchzit-server-$V-linux-amd64" "dist/watchzit-server-$V-windows-amd64.exe" \
  "dist/watchzit-agent-$V-windows-amd64.exe" "dist/watchzit-agent-$V-linux-amd64" "dist/watchzit-agent-$V-linux-arm64" "dist/watchzit-launcher-$V-windows-amd64.exe"
cp THIRD_PARTY_LICENSES.md "dist/THIRD_PARTY_LICENSES-$V.md"

# Linux packages of the server; they carry the agent installers and the
# launcher of this version under the names the console offers them by.
rm -rf bin/pkg && mkdir -p bin/pkg/downloads
cp "dist/watchzit-server-$V-linux-amd64" bin/pkg/watchzit-server
cp "dist/watchzit-agent-$V-x64.msi" bin/pkg/downloads/watchzit-agent-x64.msi
cp "dist/watchzit-agent-$V-windows-amd64.exe" bin/pkg/downloads/watchzit-agent-windows-amd64.exe
cp "dist/watchzit-launcher-$V-windows-amd64.exe" bin/pkg/downloads/watchzit-launcher.exe
for arch in amd64 arm64; do cp "dist/watchzit-agent-$V-linux-$arch" "bin/pkg/downloads/watchzit-agent-linux-$arch"; done
export VERSION=$V ARCH=amd64
NFPM="go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.0"
rm -f "dist/watchzit-server_${V}_amd64.deb" "dist/watchzit-server-${V}-1.x86_64.rpm"
$NFPM package --config packaging/linux/nfpm-server.yaml --packager deb --target dist/ >/dev/null
$NFPM package --config packaging/linux/nfpm-server.yaml --packager rpm --target dist/ >/dev/null

# What goes on GitHub, with the signed manifest.
R="dist/release-$V"
rm -rf "$R" && mkdir -p "$R"
cp "dist/watchzit-agent-$V-x64.msi" "dist/watchzit-agent-$V-windows-amd64.exe" \
   "dist/watchzit-agent-$V-linux-amd64" "dist/watchzit-agent-$V-linux-arm64" "dist/watchzit-launcher-$V-windows-amd64.exe" \
   "dist/watchzit-server-$V-linux-amd64" "dist/watchzit-server-$V-windows-amd64.exe" \
   "dist/watchzit-server_${V}_amd64.deb" "dist/watchzit-server-${V}-1.x86_64.rpm" \
   "dist/THIRD_PARTY_LICENSES-$V.md" packaging/install.sh "$R/"
(cd "$R" && sha256sum $(ls | grep -v '^SHA256SUMS') > SHA256SUMS)
if [ -f "$KEY" ]; then
  go run ./tools/relsign sign -key "$KEY" -version "$V" -dir "$R" ${NOTES:+-notes "$NOTES"}
else
  echo "note: no release key at $KEY; the release is not signed"
fi
echo "built $V"
