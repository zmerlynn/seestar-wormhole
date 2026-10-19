#!/usr/bin/env bash
# Called only by test-smb.sh inside the disposable Go container.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends samba smbclient unzip ca-certificates curl >/dev/null
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*
task_root=/tmp/smb-e2e
mkdir -p "$task_root/share/MyWorks/nested"
trap 'if [[ -n ${task_samba_pid:-} ]]; then kill "$task_samba_pid" 2>/dev/null || true; wait "$task_samba_pid" 2>/dev/null || true; fi; rm -rf "$task_root"' EXIT
printf 'independent literal fixture\n' > "$task_root/share/MyWorks/nested/small.fit"
dd if=/dev/zero of="$task_root/share/MyWorks/zeros.fit" bs=131072 count=1 status=none
chmod -R a+rX "$task_root"
task_arch=$(dpkg --print-architecture)
case "$task_arch" in arm64|amd64) ;; *) echo "Unsupported fixture architecture: $task_arch" >&2; exit 1 ;; esac
task_release=rclone-v1.75.2-linux-$task_arch
curl --fail --location --retry 3 "https://downloads.rclone.org/v1.75.2/$task_release.zip" -o "$task_root/$task_release.zip"
curl --fail --location --retry 3 https://downloads.rclone.org/v1.75.2/SHA256SUMS -o "$task_root/SHA256SUMS"
(cd "$task_root"; awk -v file="$task_release.zip" '$2 == file {print; found=1} END {if (!found) exit 1}' SHA256SUMS | sha256sum -c -)
unzip -q "$task_root/$task_release.zip" -d "$task_root"
export SEESTAR_TEST_RCLONE="$task_root/$task_release/rclone"
"$SEESTAR_TEST_RCLONE" version
smbd --version
smbd --foreground --configfile=/src/scripts/testdata/smb/smb.conf &
task_samba_pid=$!
# Readiness uses the standard client and stays entirely within the container.
for task_try in {1..50}; do
 if smbclient '//127.0.0.1/EMMC Images' -p 1445 -U guest% -c ls >/dev/null 2>&1; then break; fi
 if [[ $task_try == 50 ]]; then cat "$task_root/samba.log"; exit 1; fi
 sleep 0.1
done
export SEESTAR_TEST_SAMBA_ADDR=127.0.0.1:1445
go test -p 2 -race -count=1 -timeout=2m ./internal/proxy -run '^TestComposedSMB$' -v
