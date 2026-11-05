ARG BASE
FROM ${BASE}
# The tools a live proof that needs nested KVM or smolvm runs: smolvm, which
# starts a machine inside the job's machine (/dev/kvm, with the worker's
# nested virtualization), pasta, its user-mode network, and dnsmasq, which
# answers a nested machine's DHCP and DNS. Go is smol-builder's go layer;
# check-tools.sh fails the build when a tool is not at its pin.
USER root
ARG PASST_VERSION=0.0~git20250503.587980c-2+deb13u1
ARG DNSMASQ_VERSION=2.91-1+deb13u2
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      "passt=${PASST_VERSION}" "dnsmasq-base=${DNSMASQ_VERSION}" iproute2 \
 && rm -rf /var/lib/apt/lists/*
ARG SMOLVM_VERSION=1.33.0
ARG SMOLVM_SHA256=97e0c64d50abc407499802daa6c849ac010ef29d44e1d97664d8a78067927274
RUN curl -fsSLo /tmp/smolvm.tgz "https://github.com/smol-machines/smolvm/releases/download/v${SMOLVM_VERSION}/smolvm-${SMOLVM_VERSION}-linux-x86_64.tar.gz" \
 && echo "${SMOLVM_SHA256}  /tmp/smolvm.tgz" | sha256sum -c - \
 && mkdir -p /opt/smolvm \
 && tar -xzf /tmp/smolvm.tgz -C /opt/smolvm --strip-components=1 \
 && ln -s /opt/smolvm/smolvm /usr/local/bin/smolvm \
 && rm /tmp/smolvm.tgz
ARG GO_VERSION=1.27.3
COPY check-tools.sh nested-probe.sh /opt/live-proof/
RUN /opt/live-proof/check-tools.sh "${GO_VERSION}" "${SMOLVM_VERSION}" "${PASST_VERSION}" "${DNSMASQ_VERSION}"
# The agent harness, for root, as in the stage-0 pack (install-harness.sh).
COPY harness/ /opt/stage0-harness/
RUN /opt/stage0-harness/install-harness.sh root /root
WORKDIR /workspace
