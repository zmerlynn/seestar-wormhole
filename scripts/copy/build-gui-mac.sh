#!/usr/bin/env bash
# Build the universal seestar-copy.app, combine the two Darwin CLI parts from
# release.sh, and package, sign and notarize the DMG with package-gui-mac.sh.
# Run on a Mac with Xcode Command Line Tools and Go 1.27.3, with
# scripts/dmg-requirements.txt installed in an active Python venv. Usable
# locally and in release.yml.
set -euo pipefail
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOMAXPROCS=2
[[ $(uname -s) == Darwin ]] || { echo 'Run on macOS with Xcode Command Line Tools' >&2; exit 1; }
[[ $# == 3 && $1 == /* && $3 == /* ]] || {
 echo 'Usage: scripts/build-gui-mac.sh /absolute/parts-directory version /absolute/output-directory' >&2
 exit 2
}
[[ $(go env GOVERSION) == go1.27.3 ]] || { echo 'Go 1.27.3 required' >&2; exit 1; }
xcode-select -p >/dev/null
task_parts=$1
task_version=$2
"$(dirname "$0")/release.sh" --validate-only "$task_version"
task_bundle_version=${task_version#v}
task_bundle_version=${task_bundle_version%-rc.*}
for task_arch in arm64 amd64; do
 [[ -f "$task_parts/seestar-copy-cli_${task_version}_darwin_${task_arch}" ]] || {
  echo "Darwin $task_arch CLI part is missing from $task_parts" >&2
  exit 1
 }
done
cd "$(dirname "$0")/.."
task_temp=$(mktemp -d "${TMPDIR:-/tmp}/seestar-copy-build.XXXXXX")
trap 'rm -rf "$task_temp"' EXIT
export GOFLAGS= GOAMD64=v1 GOARM64=v8.0
export CGO_ENABLED=1 GOOS=darwin
export CC="$PWD/scripts/gui-mac-clang.sh"
export CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0"
export MACOSX_DEPLOYMENT_TARGET=13.0
xcrun clang --version
xcrun --show-sdk-version
pkgutil --pkg-info=com.apple.pkg.CLTools_Executables
# The icon is 256 px, so the icon set stops there.
task_iconset="$task_temp/seestar-copy.iconset"
mkdir -p "$task_iconset"
for task_size in 16 32 128 256; do
 sips -z "$task_size" "$task_size" cmd/seestar-copy/icon.png --out "$task_iconset/icon_${task_size}x${task_size}.png" >/dev/null
 if [[ $task_size != 256 ]]; then
  task_double=$((task_size * 2))
  sips -z "$task_double" "$task_double" cmd/seestar-copy/icon.png --out "$task_iconset/icon_${task_size}x${task_size}@2x.png" >/dev/null
 fi
done
# Both slices share one bundle's metadata and resources. Sign only after lipo.
task_universal="$task_temp/universal/seestar-copy.app"
task_contents="$task_universal/Contents"
mkdir -p "$task_contents/MacOS" "$task_contents/Resources"
iconutil -c icns "$task_iconset" -o "$task_contents/Resources/seestar-copy.icns"
# The notices travel with an app dragged out of the DMG.
cp LICENSE NOTICE "$task_contents/Resources/"
chmod 0644 "$task_contents/Resources/LICENSE" "$task_contents/Resources/NOTICE"
cat > "$task_contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>seestar-copy</string>
<key>CFBundleIdentifier</key><string>net.zml.seestar-copy</string>
<key>CFBundleIconFile</key><string>seestar-copy.icns</string>
<key>CFBundleName</key><string>seestar-copy</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>${task_bundle_version}</string>
<key>CFBundleShortVersionString</key><string>${task_bundle_version}</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict></plist>
PLIST
plutil -lint "$task_contents/Info.plist"
for task_arch in arm64 amd64; do
 task_slice="$task_temp/seestar-copy-$task_arch"
 GOARCH="$task_arch" go build -mod=vendor -p 2 -tags gui,production -trimpath -buildvcs=false \
  -ldflags="-s -w -X main.version=$task_version" -o "$task_slice" ./cmd/seestar-copy
 task_build=$(xcrun vtool -show-build "$task_slice")
 echo "$task_build"
 awk '$1 == "minos" { found=1; if ($2 != "13.0") exit 1 } END { if (!found) exit 1 }' <<< "$task_build"
 shasum -a 256 "$task_slice"
done
lipo -create "$task_temp/seestar-copy-arm64" "$task_temp/seestar-copy-amd64" \
 -output "$task_contents/MacOS/seestar-copy"
lipo "$task_contents/MacOS/seestar-copy" -verify_arch arm64 x86_64
# The CLI is release.sh's reproducible Darwin parts, combined.
lipo -create "$task_parts/seestar-copy-cli_${task_version}_darwin_arm64" \
 "$task_parts/seestar-copy-cli_${task_version}_darwin_amd64" \
 -output "$task_temp/universal/seestar-copy-cli"
lipo "$task_temp/universal/seestar-copy-cli" -verify_arch arm64 x86_64
scripts/package-gui-mac.sh "$task_universal" "$3/seestar-copy_${task_version}_darwin_universal.dmg"
