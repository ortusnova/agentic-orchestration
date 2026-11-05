#!/bin/sh
# check-tools.sh GO SMOLVM PASST DNSMASQ: fail unless each tool of the
# live-proof pack is installed at its pin. The pack's build runs it
# (Dockerfile), and a live proof may run it again in its machine to record
# the versions it ran with.
set -eu
fail=0
check() { # NAME WANT GOT
  if [ "$2" = "$3" ]; then
    echo "$1 $3"
  else
    echo "$1: want $2, got ${3:-nothing}" >&2
    fail=1
  fi
}
check go "go$1" "$(go version 2>/dev/null | awk '{print $3}')"
check smolvm "smolvm $2" "$(smolvm --version 2>/dev/null)"
check passt "$3" "$(dpkg-query -W -f '${Version}' passt 2>/dev/null)"
check dnsmasq-base "$4" "$(dpkg-query -W -f '${Version}' dnsmasq-base 2>/dev/null)"
command -v pasta > /dev/null || { echo "pasta is not on PATH" >&2; fail=1; }
command -v dnsmasq > /dev/null || { echo "dnsmasq is not on PATH" >&2; fail=1; }
exit $fail
