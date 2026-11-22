# Sortie, from the sortie/ submodule (the fork's plan-bench), with
# acp-broker, which Sortie starts in front of each session
# (environment.host_prefix), and cp-probe for the health check.
# Build from the repository root: make -C control-plane images.
FROM docker.io/library/golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
ARG REVISION=unknown
WORKDIR /src
COPY sortie/ sortie/
COPY acp-broker/ acp-broker/
COPY control-plane/ control-plane/
RUN cd sortie && go build -tags osusergo,netgo -ldflags "-s -w -X main.Commit=${REVISION}" -o /out/sortie ./cmd/sortie
RUN cd acp-broker && go build -ldflags "-s -w" -o /out/acp-broker ./cmd/acp-broker
RUN cd control-plane && go build -ldflags "-s -w" -o /out/cp-probe ./cmd/cp-probe

FROM docker.io/library/debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
ARG REVISION=unknown
LABEL org.opencontainers.image.title="sortie" \
      org.opencontainers.image.source="https://github.com/ortusnova/agentic-orchestration" \
      org.opencontainers.image.revision="${REVISION}"
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --user-group --home-dir /var/lib/sortie --create-home sortie \
 && install -d -o sortie -g sortie -m 0700 /var/lib/sortie/run
# Git reaches the forge over HTTPS with Sortie's token, read from its
# environment at each call; the file holds no secret.
COPY control-plane/images/sortie-gitconfig /etc/gitconfig
COPY --from=build /out/ /usr/local/bin/
ENV HOME=/var/lib/sortie XDG_RUNTIME_DIR=/var/lib/sortie/run
USER sortie
WORKDIR /var/lib/sortie
ENTRYPOINT ["/usr/local/bin/sortie"]
