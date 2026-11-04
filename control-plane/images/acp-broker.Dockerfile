# acp-broker on its own. Sortie's and the workbench's images carry the same
# broker, built from the same source, since each starts one per session.
# Build from the repository root: make -C control-plane images.
FROM docker.io/library/golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
WORKDIR /src
COPY acp-broker/ acp-broker/
RUN cd acp-broker && go build -ldflags "-s -w" -o /out/acp-broker ./cmd/acp-broker

FROM gcr.io/distroless/base-nossl-debian12:nonroot@sha256:be40c00dfabd86576d92666e87e406714d5618342de1a0c213ad232de255172e
ARG REVISION=unknown
LABEL org.opencontainers.image.title="acp-broker" \
      org.opencontainers.image.source="https://github.com/ortusnova/agentic-orchestration" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/acp-broker /usr/local/bin/acp-broker
ENV XDG_RUNTIME_DIR=/tmp
ENTRYPOINT ["/usr/local/bin/acp-broker"]
