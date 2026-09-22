#!/bin/sh
# Constraint 5: no environment-specific values in images. The console's API addresses
# arrive here, at container start, and are written where nginx serves them as /config.js.
#
# /tmp, not the web root: nginx.conf is deliberately arranged so the container can run
# with a read-only root filesystem, and writing into /usr/share/nginx/html would undo that.
set -eu

CONFIG_PATH=/tmp/config.js

esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

cat > "$CONFIG_PATH" <<EOF
window.__MOPAY_CONFIG__ = {
  apiBaseUrl: "$(esc "${API_BASE_URL:-}")",
  merchantApiBaseUrl: "$(esc "${MERCHANT_API_BASE_URL:-}")"
};
EOF

if [ -z "${API_BASE_URL:-}" ] || [ -z "${MERCHANT_API_BASE_URL:-}" ]; then
  echo "mopay-console: warning: API_BASE_URL and/or MERCHANT_API_BASE_URL not set;" \
       "the console will fall back to localhost defaults" >&2
fi

exec "$@"
