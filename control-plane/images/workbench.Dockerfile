# The plan workbench: the wheel with its frontend, on a Python that uv
# installs, with acp-broker (one per planning session), the openspec CLI the
# validator runs, git and cp-probe.
# Build from the repository root: make -C control-plane images.
FROM docker.io/library/node:26.12.0-bookworm@sha256:bfb78a64f53d36c691f27dceecc9d4e73b3526411e1cee6fdf8d981d06fb2eb7 AS frontend
WORKDIR /src/workbench/frontend
COPY workbench/frontend/package.json workbench/frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY workbench/frontend/ ./
RUN npx vite build --logLevel warn

FROM docker.io/library/node:26.12.0-bookworm@sha256:bfb78a64f53d36c691f27dceecc9d4e73b3526411e1cee6fdf8d981d06fb2eb7 AS wheel
ENV UV_PYTHON_INSTALL_DIR=/opt/python UV_PYTHON_PREFERENCE=only-managed UV_LINK_MODE=copy
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip && rm -rf /var/lib/apt/lists/* \
 && pip install --no-cache-dir --break-system-packages 'uv>=0.9'
WORKDIR /src/workbench
COPY workbench/ ./
COPY --from=frontend /src/workbench/frontend/dist/ frontend/dist/
RUN uv python install 3.12 \
 && uv build --wheel --python 3.12 \
 && uv export --frozen --no-dev --no-emit-project --no-hashes -o /tmp/requirements.txt \
 && uv venv --python 3.12 /opt/workbench \
 && uv pip install --python /opt/workbench/bin/python -r /tmp/requirements.txt \
 && uv pip install --python /opt/workbench/bin/python --no-deps dist/*.whl

FROM docker.io/library/golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS go
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
WORKDIR /src
COPY acp-broker/ acp-broker/
COPY control-plane/ control-plane/
RUN cd acp-broker && go build -ldflags "-s -w" -o /out/acp-broker ./cmd/acp-broker
RUN cd control-plane && go build -ldflags "-s -w" -o /out/cp-probe ./cmd/cp-probe

FROM docker.io/library/node:26.12.0-bookworm@sha256:bfb78a64f53d36c691f27dceecc9d4e73b3526411e1cee6fdf8d981d06fb2eb7
ARG REVISION=unknown
LABEL org.opencontainers.image.title="workbench" \
      org.opencontainers.image.source="https://github.com/ortusnova/agentic-orchestration" \
      org.opencontainers.image.revision="${REVISION}"
RUN npm install -g --no-audit --no-fund @fission-ai/openspec@1.13.0 \
 && useradd --system --uid 10002 --user-group --home-dir /var/lib/workbench --create-home workbench
COPY --from=wheel /opt/python/ /opt/python/
COPY --from=wheel /opt/workbench/ /opt/workbench/
COPY --from=go /out/ /usr/local/bin/
ENV PATH=/opt/workbench/bin:/usr/local/bin:/usr/bin:/bin HOME=/var/lib/workbench XDG_RUNTIME_DIR=/var/lib/workbench/run
USER workbench
WORKDIR /var/lib/workbench
CMD ["workbench", "serve", "--host", "0.0.0.0", "--port", "8793"]
