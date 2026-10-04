#!/bin/sh
set -u

IPFS_REPO="${IPFS_PATH:-/data/ipfs}"
IPFS_API_URL="http://127.0.0.1:5001"
STORAGE_MAX="${STORAGE_MAX:-20GB}"
# These GC policy values are intentionally fixed and cannot be overridden.
STORAGE_GC_WATERMARK=90
STORAGE_GC_PERIOD=1h

if [ ! -f "$IPFS_REPO/config" ]; then
	ipfs init --profile=lowpower
fi
ipfs config --json Routing.Type '"dhtclient"'
if ! ipfs config --json Datastore.StorageMax "\"$STORAGE_MAX\"" \
	|| ! ipfs config --json Datastore.StorageGCWatermark "$STORAGE_GC_WATERMARK" \
	|| ! ipfs config --json Datastore.GCPeriod "\"$STORAGE_GC_PERIOD\""; then
	echo "originless: invalid storage GC configuration" >&2
	exit 1
fi
echo "originless: storage max=$STORAGE_MAX, GC watermark=$STORAGE_GC_WATERMARK%, period=$STORAGE_GC_PERIOD"

ipfs daemon --enable-gc &
ipfs_pid=$!
app_pid=""

cleanup() {
	kill -TERM "$ipfs_pid" 2>/dev/null || true
}

# Forward container stop signals to both children so the Go app runs its
# graceful shutdown (cancel ctx, close the event DB, drain HTTP handlers).
on_signal() {
	if [ -n "$app_pid" ]; then
		kill -TERM "$app_pid" 2>/dev/null || true
	fi
	kill -TERM "$ipfs_pid" 2>/dev/null || true
}

trap on_signal TERM INT

# Wait for Kubo's HTTP RPC before starting the application. Kubo rejects GET
# requests on its API, so use an empty POST to the version endpoint.
until curl -fsS -X POST "$IPFS_API_URL/api/v0/version" >/dev/null 2>&1; do
	if ! kill -0 "$ipfs_pid" 2>/dev/null; then
		echo "originless: ipfs daemon stopped before becoming ready" >&2
		exit 1
	fi
	sleep 5
done

echo "originless: ipfs daemon ready"

/usr/local/bin/originless &
app_pid=$!
wait "$app_pid"
app_status=$?

cleanup
wait "$ipfs_pid" 2>/dev/null || true
exit "$app_status"
