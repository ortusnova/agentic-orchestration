#!/bin/sh
# nested-probe.sh ALLOWED DENIED: the measurement of the nested profile
# (README.md "Live proofs"), run in a job machine of the live-proof pack: it
# starts a smolvm machine inside this machine, with an egress policy that
# declares ALLOWED alone, and prints, with every command it ran and its
# output:
#   nested boot: booted|failed
#   nested allowed: reached|refused     (ALLOWED on port 443)
#   nested denied: reached|refused      (DENIED on port 443)
# It nests when the machine boots, reaches ALLOWED and is refused DENIED.
# ALLOWED must be a host this job machine reaches too. The machine runs
# smolvm's bare VM, which needs no image pull, and is deleted on exit.
set -u
allowed=${1:?usage: nested-probe.sh ALLOWED DENIED}
denied=${2:?usage: nested-probe.sh ALLOWED DENIED}
run() { echo "\$ $*"; "$@" 2>&1; echo "(exit $?)"; }

run smolvm --version
run ls -l /dev/kvm
run sh -c 'grep -o -m1 -w "vmx\|svm" /proc/cpuinfo'
out=$(mktemp)
# shellcheck disable=SC2016 # the nested machine expands them
probe='echo nested-vm booted; uname -r
for h in "$ALLOWED" "$DENIED"; do
  if nc -w 10 "$h" 443 < /dev/null > /dev/null 2>&1; then echo "nested-vm $h: reached"; else echo "nested-vm $h: refused"; fi
done'
echo "\$ smolvm machine run --net-backend virtio-net --block-private --deny-cidr 10.0.0.0/8 --deny-cidr 172.16.0.0/12 --deny-cidr 192.168.0.0/16 --allow-host-pattern $allowed --timeout 180s -e ALLOWED=$allowed -e DENIED=$denied -- sh -c '<probe>'"
smolvm machine run --net-backend virtio-net --block-private \
  --deny-cidr 10.0.0.0/8 --deny-cidr 172.16.0.0/12 --deny-cidr 192.168.0.0/16 \
  --allow-host-pattern "$allowed" --timeout 180s \
  -e "ALLOWED=$allowed" -e "DENIED=$denied" -- sh -c "$probe" > "$out" 2>&1
echo "(exit $?)"
cat "$out"
if grep -q '^nested-vm booted' "$out"; then echo "nested boot: booted"; else echo "nested boot: failed"; fi
for kind in allowed denied; do
  if [ "$kind" = allowed ]; then h=$allowed; else h=$denied; fi
  if grep -q "^nested-vm $h: reached" "$out"; then echo "nested $kind: reached"; else echo "nested $kind: refused"; fi
done
rm -f "$out"
