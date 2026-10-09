FROM --platform=$BUILDPLATFORM node:22-bookworm AS frontend
WORKDIR /src/frontend
COPY ddp.yaml /src/ddp.yaml
COPY frontend/package*.json ./
COPY frontend/packages ./packages
COPY frontend/apps ./apps
RUN npm ci
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY frontend/embed.go ./frontend/embed.go
COPY --from=frontend /src/frontend/dist ./frontend/dist
ARG REVISION=dev
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/Profreshor/Dealership-Data-Platform/internal/ddp/buildinfo.Version=${REVISION}" -o /out/ddp ./cmd/ddp

FROM ghcr.io/astral-sh/uv:0.12.9 AS uv
FROM python:3.12-slim-trixie AS runtime
COPY --from=uv /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends postgresql-client-17 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 ddp && useradd --uid 10001 --gid ddp --no-create-home ddp \
    && mkdir /backup-staging && chown ddp:ddp /backup-staging
WORKDIR /opt/ddp
COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev --no-install-project --python /usr/local/bin/python
COPY ddp ./ddp
COPY client ./client
COPY jobs ./jobs
COPY templates ./templates
COPY ddp.yaml ./ddp.yaml
# A template with no SQL models must also build.
FROM runtime AS source
COPY . /src
RUN mkdir -p /src/models && cp -r /src/models /opt/ddp/models

FROM runtime AS final
COPY --from=source /opt/ddp/models ./models
COPY --from=build /out/ddp /usr/local/bin/ddp
ENV PATH="/opt/ddp/.venv/bin:${PATH}" PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
USER ddp
EXPOSE 8080
ENTRYPOINT ["ddp"]
CMD ["api"]
