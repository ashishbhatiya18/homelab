#!/bin/sh
# Copy syncthing identity certs from read-only secrets mount into config dir.
# Using cp (not symlinks) so syncthing can write to /config normally without
# touching the canonical cert in secrets.
for f in cert.pem key.pem https-cert.pem https-key.pem; do
    cp "/secrets/$f" "/config/$f"
done

# Inject stable device ID from secrets into config.xml template.
DEVICE_ID=$(cat /secrets/device-id)
sed -i "s/SELF_DEVICE_ID/$DEVICE_ID/g" /config/config.xml
