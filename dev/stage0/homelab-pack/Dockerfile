ARG BASE
FROM ${BASE}
# homelab's sandbox image, as scripts/smolvm/image/Dockerfile builds it: the
# tools the CI canary installs, the guest user brian (uid 1000, sudo,
# rootless podman with its storage on /workspace), the systemctl shim, and
# the flight recorder.
USER root
COPY homelab/ /opt/homelab-sandbox/
RUN /opt/homelab-sandbox/install-tools.sh \
 && /opt/homelab-sandbox/guest-setup.sh \
 && rm -rf /var/lib/apt/lists/*
# The agent harness, for brian. A pack does not carry the image's USER: a
# machine runs its commands as root unless it is created with `--user brian`
# (measured on smolvm 1.32.4). With that, every command runs as brian, with
# HOME=/home/brian, and /workspace, the machine's own disk (mode 1777), takes
# Sortie's seeding as brian. WORKFLOW-homelab.md sets `user: brian`
# (Sortie's environment.smolvm.user; design D12, amended 2026-10-31).
COPY harness/ /opt/stage0-harness/
RUN /opt/stage0-harness/install-harness.sh brian /home/brian
WORKDIR /workspace
