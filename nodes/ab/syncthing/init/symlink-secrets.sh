#!/bin/sh
# Symlink syncthing identity certs from read-only secrets mount into config dir
for f in cert.pem key.pem https-cert.pem https-key.pem; do
    ln -sf "/secrets/$f" "/config/$f"
done
