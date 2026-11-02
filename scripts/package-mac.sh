#!/usr/bin/env bash
# Combine the two Darwin CLI parts from release.sh into a universal binary,
# sign it, and package, notarize and staple the DMG. Run on a Mac with Xcode
# Command Line Tools, with scripts/dmg-requirements.txt installed in an active
# Python venv. Usable locally and in release.yml.
# Never trace credentials, even when invoked by a shell with xtrace enabled.
set +x
set -euo pipefail
[[ $(uname -s) == Darwin ]] || { echo 'Run on macOS with Xcode Command Line Tools' >&2; exit 1; }
[[ $# == 3 && $1 == /* && $3 == /* ]] || {
 echo 'Usage: scripts/package-mac.sh /absolute/parts-directory version /absolute/output-directory' >&2
 exit 2
}
task_scripts=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
"$task_scripts/release.sh" --validate-only "$2"
command -v dmgbuild >/dev/null 2>&1 || {
 echo 'dmgbuild is required: python3 -m pip install --require-hashes --only-binary :all: -r scripts/dmg-requirements.txt' >&2
 exit 1
}
task_parts=$1
task_version=$2
task_dmg="$3/seestar-copy_${task_version}_darwin_universal.dmg"
for task_arch in arm64 amd64; do
 [[ -f "$task_parts/seestar-copy-cli_${task_version}_darwin_${task_arch}" ]] || {
  echo "Darwin $task_arch CLI part is missing from $task_parts" >&2
  exit 1
 }
done
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
task_temp=$(mktemp -d "${TMPDIR:-/tmp}/seestar-copy-package.XXXXXX")
task_keychain=${MACOS_KEYCHAIN_PATH:-"$task_temp/signing.keychain-db"}
task_created_keychain=false
task_old_keychains=()
cleanup() {
 if (( ${#task_old_keychains[@]} )); then security list-keychains -d user -s "${task_old_keychains[@]}" || true; fi
 if "$task_created_keychain"; then security delete-keychain "$task_keychain" >/dev/null 2>&1 || true; fi
 rm -rf "$task_temp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# The DMG payload must be readable by every user; credentials stay private.
mkdir -p "$task_temp/image"
chmod 0755 "$task_temp/image"
task_cli="$task_temp/image/seestar-copy-cli"
lipo -create "$task_parts/seestar-copy-cli_${task_version}_darwin_arm64" \
 "$task_parts/seestar-copy-cli_${task_version}_darwin_amd64" -output "$task_cli"
lipo "$task_cli" -verify_arch arm64 x86_64
chmod 0755 "$task_cli"
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
 # codesign looks up the identity through the user search list; cleanup restores the list.
 while IFS= read -r task_entry; do task_entry=${task_entry#*\"}; task_old_keychains+=("${task_entry%\"}"); done \
  < <(security list-keychains -d user)
 security list-keychains -d user -s "$task_keychain" ${task_old_keychains[@]+"${task_old_keychains[@]}"}
 security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$task_password" "$task_keychain" >/dev/null
 # A static Go CLI needs no entitlements under the hardened runtime.
 codesign --force --sign "$MACOS_SIGN_IDENTITY" --keychain "$task_keychain" --options runtime --timestamp "$task_cli"
 task_team=$(codesign -dv --verbose=4 "$task_cli" 2>&1 | awk -F= '$1 == "TeamIdentifier" {print $2}')
 [[ "$task_team" == "$APPLE_TEAM_ID" ]] || { echo 'Signing certificate team does not match APPLE_TEAM_ID' >&2; exit 1; }
else
 echo 'No Developer ID credentials: ad-hoc signing; this DMG is not notarized.'
 codesign --force --sign - "$task_cli"
fi
codesign --verify --strict --verbose=2 "$task_cli"
cp "$task_scripts/../LICENSE" "$task_scripts/../NOTICE" "$task_temp/image/"
chmod 0644 "$task_temp/image/LICENSE" "$task_temp/image/NOTICE"
mkdir -p "$3"
(
 umask 022
 dmgbuild -s "$task_scripts/dmg-settings.py" -D "stage=$task_temp/image" seestar-copy "$task_dmg"
)
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
