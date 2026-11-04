# The live tests' fake model API (fakemodel/), only ever built by
# livetest/run.sh. Build from the repository root.
FROM docker.io/library/golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
WORKDIR /src
COPY control-plane/ control-plane/
RUN cd control-plane && go build -o /out/fakemodel ./livetest/fakemodel

FROM gcr.io/distroless/base-nossl-debian12:nonroot@sha256:be40c00dfabd86576d92666e87e406714d5618342de1a0c213ad232de255172e
COPY --from=build /out/fakemodel /usr/local/bin/fakemodel
ENTRYPOINT ["/usr/local/bin/fakemodel"]
