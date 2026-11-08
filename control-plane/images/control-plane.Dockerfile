# The control plane's own tools: cpctl, the egress gate (cp-egress) and
# cp-probe. Build from the repository root: make -C control-plane images.
FROM docker.io/library/golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
WORKDIR /src
COPY control-plane/ control-plane/
RUN cd control-plane && go build -ldflags "-s -w" -o /out/ ./cmd/cpctl ./cmd/cp-egress ./cmd/cp-probe

FROM gcr.io/distroless/base-nossl-debian12:nonroot@sha256:be40c00dfabd86576d92666e87e406714d5618342de1a0c213ad232de255172e
ARG REVISION=unknown
LABEL org.opencontainers.image.title="control-plane" \
      org.opencontainers.image.source="https://github.com/ortusnova/agentic-orchestration" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/ /usr/local/bin/
ENV PATH=/usr/local/bin
ENTRYPOINT []
CMD ["cp-egress"]
