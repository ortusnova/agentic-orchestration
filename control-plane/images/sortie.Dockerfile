# Sortie, from the sortie/ submodule (the fork's plan-bench), with
# acp-broker, which Sortie starts in front of each session
# (environment.host_prefix), and cp-probe for the health check.
# Build from the repository root: make -C control-plane images.
FROM docker.io/library/golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
ARG REVISION=unknown
WORKDIR /src
COPY sortie/ sortie/
COPY acp-broker/ acp-broker/
COPY control-plane/ control-plane/
RUN cd sortie && go build -tags osusergo,netgo -ldflags "-s -w -X main.Commit=${REVISION}" -o /out/sortie ./cmd/sortie
RUN cd acp-broker && go build -ldflags "-s -w" -o /out/acp-broker ./cmd/acp-broker
RUN cd control-plane && go build -ldflags "-s -w" -o /out/cp-probe ./cmd/cp-probe

FROM docker.io/library/debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
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
