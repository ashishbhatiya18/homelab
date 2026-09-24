# Hardened, non-root Claude Code terminal image.
#
# Uses a self-hosted xterm.js + node-pty terminal server instead of ttyd:
# ttyd 1.7.7 (the latest release) has a real clipboard bug — it copies
# selections via document.execCommand('copy'), but xterm.js keeps
# selections purely internal (painted to canvas), so there's no DOM
# selection for that call to act on, and copy silently does nothing.
# Building directly on xterm.js lets the clipboard addon (OSC 52) and a
# small selection->clipboard bridge be wired in correctly from the start.
#
# Not a literal "distroless" build: Claude Code's Bash tool execs via a
# shell, so a minimal shell is kept. Everything else (package manager,
# docs, unused interpreters, node-pty's native build toolchain) is
# stripped from the final stage to keep the runtime surface small.

FROM node:20-bookworm-slim AS builder

# node-pty needs a native build toolchain (make/g++/python3) to compile
# its addon if no prebuilt binary matches this platform. This entire
# stage is discarded in the final image.
RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
        python3 \
        make \
        g++ \
    && rm -rf /var/lib/apt/lists/*

# Install claude-code via the official native installer (not `npm install
# -g`) — the npm global install is a deprecated path that claude-code
# itself now flags as a "leftover installation" at startup.
USER node
ENV HOME=/home/node
RUN curl -fsSL https://claude.ai/install.sh | bash
USER root

WORKDIR /build/server
COPY server/package.json ./
RUN npm install --omit=dev \
    && npm install --no-save \
        @xterm/xterm@6.0.0 \
        @xterm/addon-fit@0.11.0 \
        @xterm/addon-clipboard@0.2.0 \
        @xterm/addon-web-links@0.12.0 \
        @xterm/addon-search@0.16.0 \
        @xterm/addon-unicode11@0.9.0 \
        @xterm/addon-webgl@0.19.0 \
        @xterm/addon-serialize@0.14.0

COPY server/server.js ./
COPY server/public ./public

# Vendor the xterm.js frontend assets into public/vendor so nothing is
# fetched from a CDN at runtime (self-hosted, matches everything else in
# this repo — and the CSP-restricted environments this may sit behind).
RUN mkdir -p public/vendor \
    && cp node_modules/@xterm/xterm/css/xterm.css public/vendor/ \
    && cp node_modules/@xterm/xterm/lib/xterm.js public/vendor/ \
    && cp node_modules/@xterm/addon-fit/lib/addon-fit.js public/vendor/ \
    && cp node_modules/@xterm/addon-clipboard/lib/addon-clipboard.js public/vendor/ \
    && cp node_modules/@xterm/addon-web-links/lib/addon-web-links.js public/vendor/ \
    && cp node_modules/@xterm/addon-search/lib/addon-search.js public/vendor/ \
    && cp node_modules/@xterm/addon-unicode11/lib/addon-unicode11.js public/vendor/ \
    && cp node_modules/@xterm/addon-webgl/lib/addon-webgl.js public/vendor/ \
    && cp node_modules/@xterm/addon-serialize/lib/addon-serialize.js public/vendor/ \
    && rm -rf node_modules/@xterm

FROM node:20-bookworm-slim

# Only python3 is added on top of the base node runtime, for Claude Code's
# own tool use. Once installed, the package manager itself is removed so
# nothing new can be installed at runtime (including by a compromised
# process inside the container).
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/* \
        /usr/share/doc /usr/share/man \
        /usr/bin/apt* /usr/sbin/apt* /etc/apt \
        /usr/bin/dpkg* /usr/sbin/dpkg* /var/lib/dpkg \
        /usr/lib/python3*/ensurepip

# node:20-bookworm-slim already ships a non-root "node" user at uid/gid
# 1000 — reused here so it matches PUID=1000 used by the codeserver stack
# that shares the /home/dietpi/repos workspace.
RUN mkdir -p /workspace /app \
    && chown -R node:node /workspace /app /home/node

# Only /home/node/.claude (session/auth state) is meant to be bind-mounted
# at runtime — NOT all of /home/node, which would shadow .local/bin below.
COPY --from=builder --chown=node:node /home/node/.local /home/node/.local
COPY --from=builder --chown=node:node /build/server /app

USER node
WORKDIR /workspace
ENV HOME=/home/node
ENV PATH="/home/node/.local/bin:${PATH}"
ENV PORT=7681
ENV TERMINAL_CWD=/workspace
EXPOSE 7681

ENTRYPOINT ["node", "/app/server.js"]
