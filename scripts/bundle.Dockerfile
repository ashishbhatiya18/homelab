# Node bundle: one node's stacks plus node.sh/node.conf, published by
# .github/workflows/node-bundles.yml and deployed by `home deploy`.
# Files only (FROM scratch, no RUN), so one build serves amd64 and arm64.
FROM scratch
ARG NODE
ARG REVISION
LABEL homelab.bundle=true \
      homelab.node=$NODE \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source=https://github.com/ashishbhatiya18/homelab
COPY nodes/$NODE/ /nodes/$NODE/
COPY nodes/node.sh /nodes/$NODE/node.sh
