#!/usr/bin/env bash
# Exercise support transitions without network access or a host Go toolchain.
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_tmp=$(mktemp -d "${TMPDIR:-/tmp}/seestar-go-support-test.XXXXXX")
trap 'rm -rf "$task_tmp"' EXIT
mkdir -p "$task_tmp/bin" "$task_tmp/scripts"
cp "$task_src/scripts/check-go-support.sh" "$task_tmp/scripts/"
cat > "$task_tmp/bin/curl" <<'MOCK'
#!/usr/bin/env bash
cat "$GO_SUPPORT_RELEASES"
exit "${GO_SUPPORT_CURL_EXIT:-0}"
MOCK
chmod +x "$task_tmp/bin/curl"
export PATH="$task_tmp/bin:$PATH" GO_SUPPORT_RELEASES="$task_tmp/releases.json"
cat > "$GO_SUPPORT_RELEASES" <<'JSON'
[{"version":"go1.28rc1","stable":false},{"version":"go1.27.3","stable":true},{"version":"go1.26.9","stable":true}]
JSON
check() {
 local want=$1 code=0
 shift
 "$task_tmp/scripts/check-go-support.sh" "$@" > "$task_tmp/output" 2>&1 || code=$?
 if [[ ( "$want" == pass && "$code" != 0 ) || ( "$want" == fail && "$code" == 0 ) ]]; then
  cat "$task_tmp/output" >&2
  printf 'exit=%s, want %s\n' "$code" "$want" >&2; exit 1
 fi
}
printf 'module fixture\n\ngo 1.27\n' > "$task_tmp/go.mod"
check pass
check pass latest
printf 'module fixture\n\ngo 1.26.1\n' > "$task_tmp/go.mod"
check pass
check fail latest
grep -Fq 'older supported release series' "$task_tmp/output"
printf 'module fixture\n\ngo 1.25\n' > "$task_tmp/go.mod"
check fail
grep -Fq 'no longer supported upstream' "$task_tmp/output"
check fail latest
printf 'module fixture\n\ngo 1.26\ntoolchain go1.27.3\n' > "$task_tmp/go.mod"
check pass latest
printf 'module fixture\n\ntoolchain go1.27.3\ngo 1.26\n' > "$task_tmp/go.mod"
check pass latest
printf 'module fixture\n\ngo 1.27\ntoolchain default\n' > "$task_tmp/go.mod"
check pass latest
# Numeric ordering must work across two-digit version components.
printf '[{"version":"go1.9.7","stable":true},{"version":"go1.10.1","stable":true}]\n' > "$GO_SUPPORT_RELEASES"
printf 'module fixture\n\ngo 1.10\n' > "$task_tmp/go.mod"
check pass latest
for response in '[]' 'not json' '[{"version":"go1.28rc1","stable":false}]'; do
 printf '%s\n' "$response" > "$GO_SUPPORT_RELEASES"
 check fail
done
printf '[{"version":"go1.10.1","stable":true}]\n' > "$GO_SUPPORT_RELEASES"
export GO_SUPPORT_CURL_EXIT=22
check fail
printf 'Go support checks passed\n'
