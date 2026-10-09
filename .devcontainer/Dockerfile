FROM golang:1.27.1-bookworm AS go
FROM node:22-bookworm AS node
FROM ghcr.io/astral-sh/uv:0.12.9 AS uv
FROM mcr.microsoft.com/devcontainers/base:bookworm
RUN apt-get update && apt-get install -y --no-install-recommends postgresql-common \
    && /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y \
    && apt-get install -y --no-install-recommends postgresql-client-17 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=uv /uv /uvx /usr/local/bin/
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ENV PATH="/home/vscode/.local/bin:/usr/local/go/bin:${PATH}"
USER vscode
RUN uv python install 3.12
WORKDIR /workspace
