#!/bin/sh
# Boots a VM nested in the job machine under libvirt (as homelab's seed),
# on libvirt's default network, and from it connects to $ALLOWED and
# $DENIED on port 443. Prints "nested allowed: reached|refused" and
# "nested denied: reached|refused". The nested VM resolves names through
# libvirt's dnsmasq, which forwards to the machine's resolver (design D9).
# The kernel and initrd are Debian's netboot installer from deb.debian.org,
# so $ALLOWED must be deb.debian.org or the machine must reach it.
set -u
W=$(mktemp -d /tmp/nested-egress.XXXXXX)
D=https://deb.debian.org/debian/dists/trixie/main/installer-amd64/current/images/netboot/debian-installer/amd64
if [ "$(id -u)" = 0 ]; then S=; else S=sudo; fi
$S /opt/homelab-sandbox/guest-start.sh "$(id -u)" >"$W/guest-start.log" 2>&1 || { echo "nested: guest-start failed"; cat "$W/guest-start.log"; }
if ! curl -sSf -o "$W/linux" "$D/linux" || ! curl -sSf -o "$W/initrd.gz" "$D/initrd.gz"; then echo "nested: kernel download failed"; exit 0; fi
cat >"$W/probe" <<EOF
#!/bin/sh
mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev 2>/dev/null
modprobe virtio_net 2>/dev/null; modprobe virtio_pci 2>/dev/null
sleep 2
ip link set lo up; ip link set eth0 up
ip addr add 192.168.122.77/24 dev eth0; ip route add default via 192.168.122.1
echo "nameserver 192.168.122.1" >/etc/resolv.conf
sleep 2
probe() { if nc -w 10 "\$1" 443 </dev/null >/dev/null 2>&1; then echo "nested-vm \$2: reached"; else echo "nested-vm \$2: refused"; fi; }
probe $ALLOWED allowed
probe $DENIED denied
echo nested-vm-done
poweroff -f
EOF
python3 - "$W" <<'EOF'
import os, sys, gzip
w = sys.argv[1]
data = open(os.path.join(w, "probe"), "rb").read()
def ent(name, mode, body, ino):
    name = name.encode() + b"\0"
    h = b"070701" + b"".join(b"%08X" % v for v in (ino, mode, 0, 0, 1, 0, len(body), 0, 0, 0, 0, len(name), 0))
    out = h + name
    out += b"\0" * ((4 - len(out) % 4) % 4) + body
    return out + b"\0" * ((4 - len(out) % 4) % 4)
arc = ent("probe", 0o100755, data, 1) + ent("TRAILER!!!", 0, b"", 0)
open(os.path.join(w, "extra.gz"), "wb").write(gzip.compress(arc))
EOF
cat "$W/initrd.gz" "$W/extra.gz" >"$W/initrd"
$S chmod 0755 "$W"; $S chmod 0644 "$W/linux" "$W/initrd"
N=nested-egress-$$
$S virt-install --connect qemu:///system --name "$N" --transient --noautoconsole \
  --memory 512 --vcpus 1 --osinfo detect=on,require=off --disk none --network network=default,model=virtio \
  --graphics none --serial "file,path=$W/console.log" \
  --boot "kernel=$W/linux,initrd=$W/initrd,kernel_args=console=ttyS0 rdinit=/probe quiet" >"$W/virt-install.log" 2>&1 \
  || { echo "nested: virt-install failed"; cat "$W/virt-install.log"; exit 0; }
i=0
while [ $i -lt 120 ] && ! $S grep -q nested-vm-done "$W/console.log" 2>/dev/null; do sleep 2; i=$((i + 1)); done
$S virsh -c qemu:///system destroy "$N" >/dev/null 2>&1
$S grep -q nested-vm-done "$W/console.log" 2>/dev/null || { echo "nested: the VM printed no result"; $S tail -20 "$W/console.log"; }
$S sed -n 's/^nested-vm \(allowed\|denied\): /nested \1: /p' "$W/console.log" | tr -d '\r'
