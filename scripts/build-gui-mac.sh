#!/usr/bin/env bash
# Run on a Mac from an extracted git archive, with the checked-in vendor tree.
set -euo pipefail
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOMAXPROCS=2
[[ $(uname -s) == Darwin ]] || { echo 'Run on macOS with Xcode Command Line Tools' >&2; exit 1; }
[[ $(go env GOVERSION) == go1.27.3 ]] || { echo 'Go 1.27.3 required' >&2; exit 1; }
xcode-select -p >/dev/null
: "${1:?Usage: scripts/build-gui-mac.sh /absolute/output/directory}"
case "$1" in /*) ;; *) echo 'Output directory must be absolute' >&2; exit 1;; esac
cd "$(dirname "$0")/.."
export GOFLAGS= GOAMD64=v1 GOARM64=v8.0
export CGO_ENABLED=1 GOOS=darwin
export CC="$PWD/scripts/gui-mac-clang.sh"
export CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0"
export MACOSX_DEPLOYMENT_TARGET=13.0
xcrun clang --version
xcrun --show-sdk-version
pkgutil --pkg-info=com.apple.pkg.CLTools_Executables
for task_arch in arm64 amd64; do
 task_app="$1/$task_arch/seestar-wormhole.app/Contents"
 mkdir -p "$task_app/MacOS" "$task_app/Resources"
 task_iconset="$1/$task_arch/wormhole.iconset"
 mkdir -p "$task_iconset"
 for task_size in 16 32 128 256 512; do
  sips -z "$task_size" "$task_size" assets/icon/icon-1024.png --out "$task_iconset/icon_${task_size}x${task_size}.png" >/dev/null
  task_double=$((task_size * 2))
  sips -z "$task_double" "$task_double" assets/icon/icon-1024.png --out "$task_iconset/icon_${task_size}x${task_size}@2x.png" >/dev/null
 done
 iconutil -c icns "$task_iconset" -o "$task_app/Resources/wormhole.icns"
 rm -r "$task_iconset"
 GOARCH="$task_arch" go build -mod=vendor -p 2 -tags gui,production -trimpath -buildvcs=false \
  -ldflags='-s -w' -o "$task_app/MacOS/seestar-wormhole" ./cmd/seestar-wormhole
 cat > "$task_app/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>seestar-wormhole</string>
<key>CFBundleIdentifier</key><string>net.zml.seestar-wormhole</string>
<key>CFBundleIconFile</key><string>wormhole.icns</string>
<key>CFBundleName</key><string>seestar-wormhole</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>0.0.0</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict></plist>
PLIST
 task_build=$(xcrun vtool -show-build "$task_app/MacOS/seestar-wormhole")
 echo "$task_build"
 awk '$1 == "minos" { found=1; if ($2 != "13.0") exit 1 } END { if (!found) exit 1 }' <<< "$task_build"
 plutil -lint "$task_app/Info.plist"
 shasum -a 256 "$task_app/MacOS/seestar-wormhole"
done
