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
# The agent harness, for brian.
COPY harness/ /opt/stage0-harness/
RUN /opt/stage0-harness/install-harness.sh brian /home/brian
# smolvm runs every command as the image's user, so Sortie's seeding, hooks,
# agent and export all run as the chain's user (design D12). Sortie seeds the
# repository into /workspace.
RUN install -d -o brian -g brian /workspace
USER brian
WORKDIR /workspace
