#!/bin/sh
# Build the node binary for each platform into dist/.
# Central serves these files to new nodes at GET /join/bin/<os>-<arch>.
set -eu
cd "$(dirname "$0")"
out="$PWD/dist"
mkdir -p "$out"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  echo "build mooch-node-$goos-$goarch"
  (cd ../Client && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -o "$out/mooch-node-$goos-$goarch" ./cmd/mooch-node)
done
