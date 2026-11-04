#!/usr/bin/env bash
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mode=${1:-supported}
case "$mode" in
 supported|latest) ;;
 *) printf 'Usage: %s [supported|latest]\n' "$0" >&2; exit 2 ;;
esac
# Compare release series, not patch versions. A toolchain directive takes priority.
series=$(awk '
 $1 == "go" { version = $2 }
 $1 == "toolchain" && $2 != "default" { toolchain = substr($2, 3) }
 END {
  if (toolchain != "") version = toolchain
  split(version, parts, "."); print parts[1] "." parts[2]
 }
' "$task_src/go.mod")
[[ "$series" =~ ^[0-9]+\.[0-9]+$ ]] || { echo 'Cannot read Go release series from go.mod' >&2; exit 1; }
supported=$(curl --fail --silent --show-error --max-time 30 'https://go.dev/dl/?mode=json' | jq -er '
 [.[] | select(.stable == true) | .version
  | capture("^go(?<series>[0-9]+\\.[0-9]+)\\.[0-9]+$") | .series]
 | unique | sort_by(split(".") | map(tonumber))
 | if length == 0 then error("no supported stable Go releases") else .[] end
')
if ! printf '%s\n' "$supported" | grep -Fxq "$series"; then
 echo "::error::Go $series is no longer supported upstream. Update the repository Go toolchain and build pins to a supported release series." >&2
 exit 1
fi
latest=${supported##*$'\n'}
if [[ "$mode" == latest && "$series" != "$latest" ]]; then
 echo "::error::Go $series is the older supported release series. Upgrade the repository Go toolchain and build pins to Go $latest before upstream support ends." >&2
 exit 1
fi
printf 'Go %s passes the %s release-series check\n' "$series" "$mode"
