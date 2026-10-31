# docker build -f cli/jobimage/Dockerfile <dir holding the linux ocel and ocel-job binaries>
FROM golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4 AS go
FROM oven/bun:1.4.3-slim@sha256:14a223fa35747ec728bce300ef85888bc5f7958164d15bbba6a54b22b52a3288 AS bun
FROM ghcr.io/astral-sh/uv:0.13.4@sha256:9f130421e930bfbdc563c00740466603f8723158e90297d07957c2294640262b AS uv
FROM rust:1.99.1-slim-trixie@sha256:b8e062b1c3a80183df83d9ba187cadd2b5ab84bc78d453dbfeb2fccd5f982247 AS rust
FROM docker:29.9.1-cli@sha256:6fae2214c3121a28d5aa639d09032f61310b7c9c75ffbad1025f261a1b621800 AS docker

FROM node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates git openssh-client curl build-essential pkg-config \
      python3 python3-pip python3-venv \
 && rm -rf /var/lib/apt/lists/*

COPY --from=go /usr/local/go /usr/local/go
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
COPY --from=uv /uv /uvx /usr/local/bin/
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker

ENV RUSTUP_HOME=/usr/local/rustup
COPY --from=rust /usr/local/rustup /usr/local/rustup
COPY --from=rust /usr/local/cargo /usr/local/cargo
RUN chmod -R a+rX /usr/local/rustup /usr/local/cargo

ENV COREPACK_HOME=/opt/corepack COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable \
 && corepack prepare pnpm@12.14.0 --activate \
 && corepack prepare yarn@4.18.1 --activate \
 && corepack prepare yarn@1.22.22 \
 && chmod -R a+rwX /opt/corepack

RUN ln -s /usr/local/bin/bun /usr/local/bin/bunx \
 && mkdir -p /opt/ocel/bin /workspace \
 && chmod 1777 /workspace

COPY --chmod=0755 ocel ocel-job /opt/ocel/bin/

ENV PATH=/opt/ocel/bin:/usr/local/go/bin:/usr/local/cargo/bin:$PATH \
    PIP_BREAK_SYSTEM_PACKAGES=1
WORKDIR /workspace
ENTRYPOINT []
CMD ["ocel-job", "--help"]
