#!/usr/bin/env bash
# Exercise address changes across a simulated network interface.
set -euo pipefail
IP=$(command -v ip)
peer() { nsenter --net=/var/run/netns/lan-discovery-peer "$@"; }
"$IP" netns add lan-discovery-peer
"$IP" link add lan0 type veth peer name phone0
"$IP" addr add 192.168.50.2/24 dev lan0
"$IP" link set lan0 up
"$IP" link set phone0 netns lan-discovery-peer
peer "$IP" link set lo up
peer "$IP" addr add 192.168.50.3/24 dev phone0
peer "$IP" link set phone0 up
for source in 198.51.100.9 10.23.45.9 192.168.50.0 192.168.50.255; do
 peer "$IP" addr add "$source/32" dev lo
 "$IP" route replace "$source/32" via 192.168.50.3 dev lan0
 done
start() {
 label=$1; bind=$2
 if test -f /evidence/probe-port; then mv /evidence/probe-port "/evidence/port-$label-previous"; fi
 LAN_DISCOVERY_NETWORK_SERVER=1 LAN_DISCOVERY_BIND="$bind" /evidence/lan-address-changes.test -test.run=^TestLANAddressChangesServer$ -test.v > "/evidence/net-$label-server.log" 2>&1 &
 server_pid=$!
 for iteration in {1..50}; do if test -f /evidence/probe-port; then break; fi; sleep 0.05; done
 test -f /evidence/probe-port
}
client() {
 cases=$1; tokens=$2
 set +e
 peer env LAN_DISCOVERY_NETWORK_CLIENT=1 LAN_DISCOVERY_CASES="$cases" LAN_DISCOVERY_TOKENS="$tokens" /evidence/lan-address-changes.test -test.run=^TestLANAddressChangesClient$ -test.v > "/evidence/net-$label-client.log" 2>&1
 result=$?
 wait "$server_pid"; server_result=$?
 set -e
 printf '%s client=%s server=%s\n' "$label" "$result" "$server_result" >> /evidence/net-status.txt
 test "$result" = 0 && test "$server_result" = 0
}
# Change a prefix after Listen. The formerly on-link requester is now routed.
peer "$IP" addr add 192.168.50.100/32 dev lo
start prefix-change 192.168.50.2
"$IP" addr del 192.168.50.2/24 dev lan0
"$IP" addr add 192.168.50.2/32 dev lan0
"$IP" route replace 192.168.50.3/32 dev lan0
"$IP" route replace 192.168.50.100/32 via 192.168.50.3 dev lan0
"$IP" addr > /evidence/prefix-change-addresses.log
"$IP" route > /evidence/prefix-change-routes.log
client '[{"Source":"192.168.50.100","Destination":"192.168.50.2","Reject":true}]' 4
# Address replacement requires restart; neither the new address nor the
# removed startup address may advertise identity or spend tokens.
"$IP" addr del 192.168.50.2/32 dev lan0
"$IP" addr add 192.168.50.2/24 dev lan0
start address-change 0.0.0.0
"$IP" addr del 192.168.50.2/24 dev lan0
"$IP" addr add 192.168.50.8/24 dev lan0
client '[{"Source":"192.168.50.3","Destination":"192.168.50.8","Reject":true},{"Source":"192.168.50.3","Destination":"255.255.255.255","Reject":true}]' 4

# After restart, the replacement address is selected and can be advertised.
start address-restart 0.0.0.0
client '[{"Source":"192.168.50.3","Destination":"192.168.50.8","Reply":"192.168.50.8"}]' 3

# Unrelated interfaces magnify netlink dump allocations during discovery.
# Exercise bounded handling of high-volume discovery traffic.
"$IP" addr del 192.168.50.8/24 dev lan0
"$IP" addr add 192.168.50.2/24 dev lan0
for ((n=0; n<64; n++)); do "$IP" link add "dummy$n" type dummy; done
export LAN_DISCOVERY_FLOOD_COUNT=5000
start malformed-flood 192.168.50.2
client '[]' 4
export LAN_DISCOVERY_EXHAUSTED_BUCKET=1
start exhausted-bucket-flood 192.168.50.2
client '[]' 0

# Valid rejected-source traffic exercises bounded handling of a routed flood.
# The server freezes its injected clock until the refill handshake.
unset LAN_DISCOVERY_EXHAUSTED_BUCKET
export LAN_DISCOVERY_REJECTED_FLOOD=1 LAN_DISCOVERY_FLOOD_SOURCE=198.51.100.9
# Removing the interface's last IPv4 address in earlier cases removed its
# routes too. Restore the routed-source return path before the valid flood.
"$IP" route replace 198.51.100.9/32 via 192.168.50.3 dev lan0
start routed-flood-refill 192.168.50.2
client '[{"Source":"192.168.50.3","Destination":"192.168.50.2","Reply":"192.168.50.2"}]' 3
export LAN_DISCOVERY_FLOOD_SOURCE=192.168.50.100
start narrowed-flood-refill 192.168.50.2
"$IP" addr del 192.168.50.2/24 dev lan0
"$IP" addr add 192.168.50.2/26 dev lan0
"$IP" route replace 192.168.50.100/32 via 192.168.50.3 dev lan0
client '[{"Source":"192.168.50.3","Destination":"192.168.50.2","Reply":"192.168.50.2"}]' 3
