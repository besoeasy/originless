#!/usr/bin/env bash
# Build Originless locally and run its test container in the foreground.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PODMAN_BIN="${PODMAN:-podman}"

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
	cat <<'EOF'
Originless Podman test runner

Builds the local image and runs it in the foreground with --rm. Press Ctrl-C
to stop it; the container is removed automatically. No host or named volume
is mounted.

Environment variables:
  IMAGE_NAME      Image tag to build (default: originless:latest)
  CONTAINER_NAME  Container name (default: originless)
  HOST            Host address to bind (default: 0.0.0.0, every interface)
  PORT            Host port for Originless (default: 3232)
  VERSION         Version build argument (default: dev)
  PODMAN          Podman executable (default: podman)

The port is published on every interface so other devices on your network can
open the shared canvas and chat; the LAN address is printed on start. Originless
has no accounts and no API token: an Ed25519 signature on an event is the only
credential, and it proves who wrote an event rather than granting permission. Run
this only on a network you trust, and use HOST=127.0.0.1 to keep it private.

Examples:
  ./podman.sh
  PORT=8080 ./podman.sh
  HOST=127.0.0.1 ./podman.sh
EOF
	exit 0
fi

if ! command -v "$PODMAN_BIN" >/dev/null 2>&1; then
	echo "missing required executable: $PODMAN_BIN" >&2
	exit 1
fi

# Best effort discovery of the address other devices on the network can reach.
# Prints nothing when it cannot be determined; the loopback URL still works.
lan_address() {
	local address=""
	if command -v ip >/dev/null 2>&1; then
		address="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')"
	fi
	if [[ -z "$address" ]] && command -v hostname >/dev/null 2>&1; then
		address="$(hostname -I 2>/dev/null | awk '{print $1}')"
	fi
	if [[ -z "$address" ]] && command -v ifconfig >/dev/null 2>&1; then
		address="$(ifconfig 2>/dev/null | awk '/inet / && $2 != "127.0.0.1" { sub(/^.*:/, "", $2); print $2; exit }')"
	fi
	if [[ -z "$address" || "$address" == "127.0.0.1" ]]; then
		return 0
	fi
	printf '%s' "$address"
}

IMAGE_NAME="${IMAGE_NAME:-originless:latest}"
CONTAINER_NAME="${CONTAINER_NAME:-originless}"
HOST="${HOST:-0.0.0.0}"
PORT="${PORT:-3232}"
VERSION="${VERSION:-dev}"

if [[ ! "$PORT" =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then
	echo "invalid PORT: $PORT (must be between 1 and 65535)" >&2
	exit 1
fi

echo "==> building $IMAGE_NAME (version $VERSION) with $PODMAN_BIN..."
"$PODMAN_BIN" build \
	--build-arg "VERSION=$VERSION" \
	--tag "$IMAGE_NAME" \
	"$SCRIPT_DIR"

echo "==> starting $CONTAINER_NAME"
if [[ "$HOST" == "0.0.0.0" || "$HOST" == "::" ]]; then
	echo "    local:   http://127.0.0.1:${PORT}"
	LAN="$(lan_address)"
	if [[ -n "$LAN" ]]; then
		echo "    network: http://${LAN}:${PORT}   <- share this on your LAN"
	else
		echo "    network: could not detect a LAN address; use the host's IP with port ${PORT}"
	fi
else
	echo "    url:     http://${HOST}:${PORT}"
fi
echo "==> press Ctrl-C to stop and remove the container"

# Forward the documented settings when the caller has set them. Using
# -e NAME without a value copies from the host environment and leaves the
# image default in place when the host does not define it.
pass_env=()
for var in STORAGE_MAX MAX_EVENTS SYNC_NODES; do
	if [[ -n "${!var:-}" ]]; then
		pass_env+=(-e "$var")
	fi
done
if [[ ${#pass_env[@]} -gt 0 ]]; then
	echo "==> forwarding: ${pass_env[*]}"
fi

exec "$PODMAN_BIN" run \
	--rm \
	--name "$CONTAINER_NAME" \
	--publish "${HOST}:${PORT}:3232/tcp" \
	"${pass_env[@]+"${pass_env[@]}"}" \
	"$IMAGE_NAME"
