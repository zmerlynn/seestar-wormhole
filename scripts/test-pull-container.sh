#!/usr/bin/env bash
# Run inside a disposable golang:1.27 container with apt access to install Samba.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends samba smbclient >/dev/null
mkdir -p /tmp/smb-e2e/share/MyWorks/nested
printf 'independent literal fixture\n' > /tmp/smb-e2e/share/MyWorks/nested/small.fit
dd if=/dev/zero of=/tmp/smb-e2e/share/MyWorks/zeros.fit bs=131072 count=1 status=none
chmod -R a+rX /tmp/smb-e2e
smbd --foreground --configfile=/src/scripts/testdata/smb/smb.conf &
task_samba_pid=$!
mkdir -p /tmp/smb-many/share/MyWorks
for task_n in {01..16}; do
 dd if=/dev/zero of="/tmp/smb-many/share/MyWorks/file-$task_n.fit" bs=131072 count=1 status=none
done
chmod -R a+rX /tmp/smb-many
sed -e 's/1445/1446/' -e 's,smb-e2e,smb-many,g' /src/scripts/testdata/smb/smb.conf > /tmp/smb-many/smb.conf
smbd --foreground --configfile=/tmp/smb-many/smb.conf &
task_many_samba_pid=$!
trap 'kill "$task_samba_pid" "$task_many_samba_pid" 2>/dev/null || true; wait "$task_samba_pid" 2>/dev/null || true; wait "$task_many_samba_pid" 2>/dev/null || true; rm -rf /tmp/smb-e2e /tmp/smb-many' EXIT
for task_try in {1..50}; do
 if smbclient '//127.0.0.1/EMMC Images' -p 1445 -U guest% -c ls >/dev/null 2>&1; then break; fi
 if [[ $task_try == 50 ]]; then cat /tmp/smb-e2e/samba.log; exit 1; fi
 sleep 0.1
done
for task_try in {1..50}; do
 if smbclient '//127.0.0.1/EMMC Images' -p 1446 -U guest% -c ls >/dev/null 2>&1; then break; fi
 if [[ $task_try == 50 ]]; then cat /tmp/smb-many/samba.log; exit 1; fi
 sleep 0.1
done
export SEESTAR_TEST_SAMBA_ADDR=127.0.0.1:1445
export SEESTAR_TEST_MANY_SAMBA_ADDR=127.0.0.1:1446
for task_package in ./internal/pull ./internal/proxy ./cmd/seestar-wormhole; do
 go test -race -count=2 -timeout=5m "$task_package" -run 'Test(Until|Repeat|Recheck|SourceRebind|First|SelectionNamespace|SelectionRegexp|RegexpState|InterruptedReceipts|CorruptionVerified|FutureMtime|RemoteChanges|ManifestInventory|DataUmask|DestinationLock|Pull|PortablePaths)' -v
done
task_no_skip_log=/tmp/smb-e2e/many-regressions.log
go test -race -count=2 -timeout=10m ./cmd/seestar-wormhole -run '^(TestPullSIGINTConcurrentCopy|TestPullReceiptInterruptRerun|TestPullCompletedSMBPeerClose)$' -v | tee "$task_no_skip_log"
if grep -Eq -- '--- SKIP: TestPull(SIGINTConcurrentCopy|ReceiptInterruptRerun|CompletedSMBPeerClose)' "$task_no_skip_log"; then
 echo '16-file regression test skipped' >&2
 exit 1
fi
for task_test in TestPullSIGINTConcurrentCopy TestPullReceiptInterruptRerun TestPullCompletedSMBPeerClose; do
 if ! grep -q -- "--- PASS: $task_test" "$task_no_skip_log"; then
  echo "16-file regression test did not pass: $task_test" >&2
  exit 1
 fi
done
