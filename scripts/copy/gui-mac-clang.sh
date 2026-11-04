#!/usr/bin/env bash
# Wails supplies an older explicit target in its cgo directives. Normalize it
# at the compiler boundary, including Go's final external link, without vendor edits.
set -euo pipefail
task_args=()
for task_arg in "$@"; do
 case "$task_arg" in -mmacosx-version-min=*) ;; *) task_args+=("$task_arg");; esac
done
exec xcrun clang "${task_args[@]}" -mmacosx-version-min=13.0
