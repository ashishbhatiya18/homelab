# Hardened, non-root ttyd + Claude Code image.
# Not a literal "distroless" build: Claude Code's Bash tool execs via a
# shell, and ttyd needs one to attach to, so a minimal shell is kept.
# Everything else (package manager, docs, unused interpreters) is stripped
# from the final stage to keep the runtime surface as small as possible.

FROM node:20-bookworm-slim AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG TTYD_VERSION=1.7.7
RUN curl -fsSL -o /usr/local/bin/ttyd \
        "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" \
    && chmod +x /usr/local/bin/ttyd

# Install via the official native installer (not `npm install -g`) — the
# npm global install is a deprecated path that claude-code itself now
# flags as a "leftover installation" at startup.
USER node
ENV HOME=/home/node
RUN curl -fsSL https://claude.ai/install.sh | bash
USER root

FROM node:20-bookworm-slim

# Only python3 is added on top of the base node runtime. Once installed,
# the package manager itself is removed so nothing new can be installed
# at runtime (including by a compromised process inside the container).
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/* \
        /usr/share/doc /usr/share/man \
        /usr/bin/apt* /usr/sbin/apt* /etc/apt \
        /usr/bin/dpkg* /usr/sbin/dpkg* /var/lib/dpkg \
        /usr/lib/python3*/ensurepip

COPY --from=builder /usr/local/bin/ttyd /usr/local/bin/ttyd

# node:20-bookworm-slim already ships a non-root "node" user at uid/gid
# 1000 — reused here so it matches PUID=1000 used by the codeserver stack
# that shares the /home/dietpi/repos workspace.
RUN mkdir -p /workspace \
    && chown -R node:node /workspace /home/node

# Only /home/node/.claude (session/auth state) is meant to be bind-mounted
# at runtime — NOT all of /home/node, which would shadow .local/bin below.
COPY --from=builder --chown=node:node /home/node/.local /home/node/.local

USER node
WORKDIR /workspace
ENV HOME=/home/node
ENV PATH="/home/node/.local/bin:${PATH}"
EXPOSE 7681

ENTRYPOINT ["ttyd", "-W", "-p", "7681", "claude"]
