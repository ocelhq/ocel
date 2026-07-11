FROM node:22-bookworm

# Install system dependencies
RUN apt-get update && apt-get install -y \
  git \
  curl \
  jq \
  ca-certificates \
  && rm -rf /var/lib/apt/lists/*

# Install Go toolchain (pinned to ocel/go.mod's `go 1.25.11`)
RUN curl -fsSL https://go.dev/dl/go1.25.11.linux-$(dpkg --print-architecture).tar.gz \
  | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:$PATH"

# Install system dependencies for Beads
RUN apt-get update && apt-get install -y \
  dpkg-dev \
  libicu72 \
  && rm -rf /var/lib/apt/lists/* \
  && ARCH_DIR=$(dpkg-architecture -qDEB_HOST_MULTIARCH) \
  && for lib in /usr/lib/$ARCH_DIR/libicu*.so.72; do \
       ln -s "$lib" "${lib%.72}.74"; \
     done

# Pinned to a specific tagged release instead of the install.sh script on
# `main`: that script always resolves whatever GitHub currently reports as
# "latest" release, so even pinning the script's own git ref wouldn't stop
# the installed `bd` version from drifting between builds. Downloads the
# same prebuilt, embedded-Dolt-capable release archive install.sh would
# have fetched (source-building instead would need CGO_ENABLED=1 and a C
# toolchain this image doesn't have), and checks it against the release's
# published checksum.
RUN set -eu; \
  BEADS_VERSION=1.1.0; \
  case "$(dpkg --print-architecture)" in \
    amd64) BEADS_SHA256=b0f3dd607c3fb989ee08d0a6854fba80d0402971eb108f9af6170bc14d491a34 ;; \
    arm64) BEADS_SHA256=e64eb6f5f998c9eae3ef9ec786f5f1c907ab3ed04fe220ebf265ca9952e21b2f ;; \
    *) echo "unsupported architecture for beads: $(dpkg --print-architecture)" >&2; exit 1 ;; \
  esac; \
  BEADS_ARCHIVE="beads_${BEADS_VERSION}_linux_$(dpkg --print-architecture).tar.gz"; \
  curl -fsSL -o "/tmp/${BEADS_ARCHIVE}" "https://github.com/gastownhall/beads/releases/download/v${BEADS_VERSION}/${BEADS_ARCHIVE}"; \
  echo "${BEADS_SHA256}  /tmp/${BEADS_ARCHIVE}" | sha256sum -c -; \
  tar -C /usr/local/bin -xzf "/tmp/${BEADS_ARCHIVE}" bd; \
  rm "/tmp/${BEADS_ARCHIVE}"; \
  ln -s /usr/local/bin/bd /usr/local/bin/beads

RUN corepack enable

# Build-args for UID/GID alignment: sandcastle docker build-image
# defaults these to the host user's UID/GID so image-built files
# and bind-mounted files share an owner without runtime chown.
ARG AGENT_UID=1000
ARG AGENT_GID=1000

# Rename the base image's "node" user to "agent" and align UID/GID.
RUN groupmod -o -g $AGENT_GID node && usermod -o -u $AGENT_UID -g $AGENT_GID -d /home/agent -m -l agent node
USER ${AGENT_UID}:${AGENT_GID}

# Install Claude Code CLI
RUN curl -fsSL https://claude.ai/install.sh | bash

# Pre-fetch pnpm at the version pinned in the repo's root package.json
# (devEngines.packageManager) so the first `pnpm install` in a run doesn't
# pay a download.
RUN corepack prepare pnpm@11.10.0 --activate

# Add Claude and Go binaries to PATH
ENV PATH="/home/agent/.local/bin:/home/agent/go/bin:$PATH"

WORKDIR /home/agent

# In worktree sandbox mode, Sandcastle bind-mounts the git worktree at
# /home/agent/workspace and overrides the working directory there at
# container start.
ENTRYPOINT ["sleep", "infinity"]
