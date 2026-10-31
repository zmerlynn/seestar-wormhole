#!/usr/bin/env bash
# Run on a Mac from an extracted git archive, with the checked-in vendor tree.
set -euo pipefail
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOMAXPROCS=2
[[ $(uname -s) == Darwin ]] || { echo 'Run on macOS with Xcode Command Line Tools' >&2; exit 1; }
[[ $(go env GOVERSION) == go1.27.3 ]] || { echo 'Go 1.27.3 required' >&2; exit 1; }
xcode-select -p >/dev/null
: "${1:?Usage: scripts/build-gui-mac.sh /absolute/output/directory [version] [revision]}"
task_version=${2:-development}
task_revision=${3:-development}
task_bundle_version=
if [[ "$task_version" != development ]]; then
 "$(dirname "$0")/release.sh" --validate-only "$task_version"
 task_bundle_version=${task_version#v}
 task_bundle_version=${task_bundle_version%-rc.*}
fi
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
  sips -z "$task_size" "$task_size" internal/icon/icon-1024.png --out "$task_iconset/icon_${task_size}x${task_size}.png" >/dev/null
  task_double=$((task_size * 2))
  sips -z "$task_double" "$task_double" internal/icon/icon-1024.png --out "$task_iconset/icon_${task_size}x${task_size}@2x.png" >/dev/null
 done
 iconutil -c icns "$task_iconset" -o "$task_app/Resources/wormhole.icns"
 rm -r "$task_iconset"
 GOARCH="$task_arch" go build -mod=vendor -p 2 -tags gui,production -trimpath -buildvcs=false \
  -ldflags="-s -w -X main.version=$task_version -X main.revision=$task_revision" -o "$task_app/MacOS/seestar-wormhole" ./cmd/seestar-wormhole
 cat > "$task_app/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>seestar-wormhole</string>
<key>CFBundleIdentifier</key><string>net.zml.seestar-wormhole</string>
<key>CFBundleIconFile</key><string>wormhole.icns</string>
<key>CFBundleName</key><string>seestar-wormhole</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>${task_bundle_version:-1}</string>
<key>CFBundleShortVersionString</key><string>${task_bundle_version:-0.0.0}</string>
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

# Both slices share the same bundle metadata and resources. Sign only after lipo.
task_universal="$1/universal/seestar-wormhole.app"
rm -rf "$task_universal"
mkdir -p "$(dirname "$task_universal")"
ditto "$1/arm64/seestar-wormhole.app" "$task_universal"
lipo -create "$1/arm64/seestar-wormhole.app/Contents/MacOS/seestar-wormhole" \
 "$1/amd64/seestar-wormhole.app/Contents/MacOS/seestar-wormhole" \
 -output "$task_universal/Contents/MacOS/seestar-wormhole"
lipo "$task_universal/Contents/MacOS/seestar-wormhole" -verify_arch arm64 x86_64
# CI downloads the reproducible portable Darwin parts here. A local build
# creates them with the same CLI build settings when no parts were supplied.
if [[ ! -d "$1/cli" ]]; then
 mkdir -p "$1/cli"
 for task_arch in arm64 amd64; do
  CGO_ENABLED=0 GOARCH="$task_arch" GOENV=off GOWORK=off GOEXPERIMENT= go build -mod=vendor -p 2 -trimpath -buildvcs=false \
   -ldflags="-s -w -X main.version=$task_version -X main.revision=$task_revision" \
   -o "$1/cli/seestar-wormhole-cli_${task_version}_darwin_${task_arch}" ./cmd/seestar-wormhole
 done
fi
lipo -create "$1/cli/seestar-wormhole-cli_${task_version}_darwin_arm64" \
 "$1/cli/seestar-wormhole-cli_${task_version}_darwin_amd64" \
 -output "$1/universal/seestar-wormhole-cli"
lipo "$1/universal/seestar-wormhole-cli" -verify_arch arm64 x86_64
scripts/package-gui-mac.sh "$task_universal" "$1/seestar-wormhole_${task_version}_darwin_universal.dmg"
