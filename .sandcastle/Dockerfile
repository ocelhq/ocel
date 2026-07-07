FROM node:22-bookworm

# Install system dependencies
RUN apt-get update && apt-get install -y \
  git \
  curl \
  jq \
  ca-certificates \
  && rm -rf /var/lib/apt/lists/*

# Install Go toolchain (pinned to ocel/go.mod's `go 1.25.3`)
RUN curl -fsSL https://go.dev/dl/go1.25.3.linux-$(dpkg --print-architecture).tar.gz \
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

RUN curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

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
