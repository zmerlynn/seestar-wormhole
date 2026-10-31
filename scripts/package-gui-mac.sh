#!/usr/bin/env bash
# Sign and package a built universal app and its sibling universal CLI. Usable locally and in release.yml.
# Never trace credentials, even when invoked by a shell with xtrace enabled.
set +x
set -euo pipefail
[[ $(uname -s) == Darwin ]] || { echo 'Run on macOS with Xcode Command Line Tools' >&2; exit 1; }
[[ $# == 2 && $1 == /* && $2 == /* && $1 == *.app && $2 == *.dmg ]] || {
 echo 'Usage: scripts/package-gui-mac.sh /absolute/seestar-wormhole.app /absolute/output.dmg' >&2
 exit 2
}
task_app=$1
task_dmg=$2
task_cli="$(dirname "$task_app")/seestar-wormhole-cli"
[[ -f "$task_cli" ]] || { echo 'Universal CLI is missing beside the app' >&2; exit 1; }
chmod 0755 "$task_cli"
task_scripts=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[[ -d "$task_app/Contents/MacOS" ]] || { echo 'App bundle is missing' >&2; exit 1; }
# All credentials absent means an explicit ad-hoc release. Partial setup fails.
task_secret_names=(MACOS_CERT_P12_BASE64 MACOS_CERT_PASSWORD MACOS_SIGN_IDENTITY APPLE_TEAM_ID APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID APPLE_NOTARY_KEY_P8_BASE64)
task_present=0
for task_name in "${task_secret_names[@]}"; do
 if [[ -n ${!task_name:-} ]]; then task_present=$((task_present + 1)); fi
done
if [[ $task_present != 0 && $task_present != ${#task_secret_names[@]} ]]; then
 echo 'Signing credentials are incomplete; provide every documented signing secret or none' >&2
 exit 1
fi
umask 077
task_temp=$(mktemp -d "${TMPDIR:-/tmp}/wormhole-package.XXXXXX")
task_keychain=${MACOS_KEYCHAIN_PATH:-"$task_temp/signing.keychain-db"}
task_created_keychain=false
cleanup() {
 if "$task_created_keychain"; then security delete-keychain "$task_keychain" >/dev/null 2>&1 || true; fi
 rm -rf "$task_temp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ $task_present != 0 ]]; then
 [[ ! -e "$task_keychain" ]] || { echo 'Temporary signing keychain already exists' >&2; exit 1; }
 task_password=$(openssl rand -hex 32)
 printf '%s' "$MACOS_CERT_P12_BASE64" | /usr/bin/base64 -D > "$task_temp/certificate.p12"
 printf '%s' "$APPLE_NOTARY_KEY_P8_BASE64" | /usr/bin/base64 -D > "$task_temp/notary.p8"
 security create-keychain -p "$task_password" "$task_keychain"
 task_created_keychain=true
 security set-keychain-settings -lut 21600 "$task_keychain"
 security unlock-keychain -p "$task_password" "$task_keychain"
 security import "$task_temp/certificate.p12" -k "$task_keychain" -P "$MACOS_CERT_PASSWORD" -T /usr/bin/codesign >/dev/null
 security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$task_password" "$task_keychain" >/dev/null
 for task_program in "$task_app" "$task_cli"; do
  codesign --force --sign "$MACOS_SIGN_IDENTITY" --keychain "$task_keychain" \
   --options runtime --timestamp --entitlements "$task_scripts/macos-entitlements.plist" "$task_program"
  task_team=$(codesign -dv --verbose=4 "$task_program" 2>&1 | awk -F= '$1 == "TeamIdentifier" {print $2}')
  [[ "$task_team" == "$APPLE_TEAM_ID" ]] || { echo 'Signing certificate team does not match APPLE_TEAM_ID' >&2; exit 1; }
 done
else
 echo 'No Developer ID credentials: ad-hoc signing; this DMG is not notarized.'
 codesign --force --sign - "$task_app"
 codesign --force --sign - "$task_cli"
fi
codesign --verify --strict --verbose=2 "$task_app"
codesign --verify --strict --verbose=2 "$task_cli"
mkdir -p "$task_temp/image" "$(dirname "$task_dmg")"
# Credentials stay private; the mounted payload must be readable by every user.
chmod 0755 "$task_temp/image"
ditto "$task_app" "$task_temp/image/seestar-wormhole.app"
cp "$task_cli" "$task_temp/image/seestar-wormhole-cli"
chmod 0755 "$task_temp/image/seestar-wormhole-cli"
ln -s /Applications "$task_temp/image/Applications"
cp "$task_scripts/../LICENSE" "$task_scripts/../NOTICE" "$task_temp/image/"
chmod 0644 "$task_temp/image/LICENSE" "$task_temp/image/NOTICE"
hdiutil create -volname seestar-wormhole -srcfolder "$task_temp/image" -ov -format UDZO "$task_dmg"
if [[ $task_present != 0 ]]; then
 codesign --force --sign "$MACOS_SIGN_IDENTITY" --keychain "$task_keychain" --timestamp "$task_dmg"
 xcrun notarytool submit "$task_dmg" --key "$task_temp/notary.p8" \
  --key-id "$APPLE_NOTARY_KEY_ID" --issuer "$APPLE_NOTARY_ISSUER_ID" --wait
 xcrun stapler staple "$task_dmg"
 xcrun stapler validate "$task_dmg"
else
 codesign --force --sign - "$task_dmg"
fi
codesign --verify --strict --verbose=2 "$task_dmg"
shasum -a 256 "$task_dmg"
