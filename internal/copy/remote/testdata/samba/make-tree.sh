#!/bin/sh
# Builds the fixture tree described by tree.txt under $1, at image build time.
set -eu
root=$1
manifest=$2
mkdir -p "$root"
grep -v '^#' "$manifest" | while IFS=' ' read -r size mtime name; do
 if [ "$size" = dir ]; then
  mkdir "$root/$name"
 else
  # seq dies of SIGPIPE once head has enough; without pipefail that is fine.
  seq -w 0 9999999 | head -c "$size" > "$root/$name"
 fi
done
# Children first, so setting a file's time cannot disturb its directory's.
grep -v '^#' "$manifest" | tac | while IFS=' ' read -r size mtime name; do
 touch -h -d "@$mtime" "$root/$name"
done
chmod -R a+rX,go-w "$root"
