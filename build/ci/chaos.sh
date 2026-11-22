#!/usr/bin/env bash
# The nightly real-process chaos run: three patchroaddb processes and a
# versitygw container on one runner, with random kills, partitions, network
# delay and S3 outages, a workload, backups and a restore check
# (TestChaos in pkg/sim). Each node runs in a network namespace of its own,
# n1 to n3 at 10.9.0.1 to 10.9.0.3, with n4 for the restore, bridged to
# the host at 10.9.0.254. Needs sudo, Docker, iproute2 and iptables.
#
# Usage: build/ci/chaos.sh <patchroaddb binary> <dir for the nodes' files>
# CHAOS_SEED and CHAOS_MINUTES (default 10) pass through to the test.
set -euo pipefail
cd "$(dirname "$0")/../.."
bin=$(realpath "$1")
dir=$(realpath -m "$2")
mkdir -p "$dir"

# versity/versitygw:v1.10.0, as build/ci/s3.sh.
image=versity/versitygw@sha256:89cad13cc981986ee284cbafea4613f9533ce6fd5f37a2c96c4c4fb8adb3a316
export AWS_ACCESS_KEY_ID=sim-access-key
export AWS_SECRET_ACCESS_KEY=sim-secret-key
export AWS_REGION=us-east-1
host=10.9.0.254
endpoint=http://$host:7070
s3=patchroaddb-chaos-s3

cleanup() {
  docker rm -f "$s3" >/dev/null 2>&1 || true
  sudo pkill -KILL -f "$bin" || true
  for i in 1 2 3 4; do sudo ip netns del "n$i" 2>/dev/null || true; done
  sudo ip link del br-sim 2>/dev/null || true
}
trap cleanup EXIT

sudo modprobe sch_netem
sudo ip link add br-sim type bridge
sudo ip addr add "$host/24" dev br-sim
sudo ip link set br-sim up
# Docker's FORWARD policy drops bridged packets that pass through iptables.
sudo iptables -I FORWARD -i br-sim -o br-sim -j ACCEPT
for i in 1 2 3 4; do
  sudo ip netns add "n$i"
  sudo ip link add "veth$i" type veth peer name eth0 netns "n$i"
  sudo ip link set "veth$i" master br-sim up
  sudo ip netns exec "n$i" ip addr add "10.9.0.$i/24" dev eth0
  sudo ip netns exec "n$i" ip link set eth0 up
  sudo ip netns exec "n$i" ip link set lo up
done

# On the host's network, so the nodes reach it at $host; docker pause
# stalls it for an S3 outage.
docker run -d --name "$s3" --network host \
  -e ROOT_ACCESS_KEY="$AWS_ACCESS_KEY_ID" -e ROOT_SECRET_KEY="$AWS_SECRET_ACCESS_KEY" \
  --tmpfs /data "$image" --port "$host:7070" posix /data >/dev/null
for i in $(seq 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/" || true)
  [[ $code != 000 ]] && break
  if [[ $i == 30 ]]; then
    echo "the S3 server did not start" >&2
    docker logs "$s3" >&2
    exit 1
  fi
  sleep 1
done

minutes=${CHAOS_MINUTES:-10}
CHAOS_BIN=$bin CHAOS_DIR=$dir CHAOS_S3=$endpoint CHAOS_S3_CONTAINER=$s3 CHAOS_MINUTES=$minutes \
  PATCHROADDB_TEST_S3_ENDPOINT=$endpoint \
  go test -run '^TestChaos$' -v -timeout "$((minutes + 30))m" ./pkg/sim
