#!/bin/sh
# ============================================================
# Entrypoint for the custom PicoClaw image (fork racso2609/picoclaw)
# ============================================================
# Fail-fast configuration checks, then exec the gateway.
#
# This entrypoint NEVER runs `picoclaw onboard`: it is an interactive wizard
# that waits for stdin and hangs forever in a container without a TTY.
# Configuration must be supplied by the operator (bind mount or env vars).
#
# Exit code 78 (EX_CONFIG from sysexits.h) is used on purpose so that a
# configuration error is distinguishable from a runtime failure.
# ============================================================
set -eu

EX_CONFIG=78

# 1. PICOCLAW_HOME must be set, exist and be writable.
if [ -z "${PICOCLAW_HOME:-}" ]; then
  echo "ERROR: PICOCLAW_HOME is not set." >&2
  echo "  It must point to the config/state directory inside the container" >&2
  echo "  (this image sets it to /home/picoclaw)." >&2
  exit "${EX_CONFIG}"
fi

if [ ! -d "${PICOCLAW_HOME}" ]; then
  echo "ERROR: PICOCLAW_HOME directory does not exist: ${PICOCLAW_HOME}" >&2
  echo "  Mount a host directory there, e.g.:" >&2
  echo "    -v \$PWD/picoclaw-data:${PICOCLAW_HOME}" >&2
  exit "${EX_CONFIG}"
fi

# Do a real write test instead of trusting `[ -w ]`: a read-only bind mount
# (`:ro`) still reports the host mode bits, so `[ -w ]` would pass and the
# failure would only surface later, far from this message.
if ! touch "${PICOCLAW_HOME}/.entrypoint-write-test" 2>/dev/null; then
  echo "ERROR: PICOCLAW_HOME is not writable: ${PICOCLAW_HOME}" >&2
  echo "  The container runs as uid/gid 1000; make the mounted directory" >&2
  echo "  owned by uid 1000 on the host, e.g.:" >&2
  echo "    chown -R 1000:1000 <host-dir>" >&2
  echo "  Also check that the mount is NOT read-only: a volume mounted with" >&2
  echo "  ':ro' cannot be fixed by chown. Drop the ':ro' suffix, e.g.:" >&2
  echo "    -v \$PWD/picoclaw-data:${PICOCLAW_HOME}" >&2
  exit "${EX_CONFIG}"
fi
rm -f "${PICOCLAW_HOME}/.entrypoint-write-test"

# 2. The config file must be provided by the operator.
# Resolve the path exactly like the binary does (cmd/picoclaw/internal/
# helpers.go: GetConfigPath() honours PICOCLAW_CONFIG first).
CONFIG_FILE="${PICOCLAW_CONFIG:-${PICOCLAW_HOME}/config.json}"

# `-r`, not `-f`: a file can exist and still be unreadable (e.g. mode 600
# owned by another uid), which would fail deep inside the gateway instead
# of here.
if [ ! -r "${CONFIG_FILE}" ]; then
  echo "ERROR: configuration file not found or not readable: ${CONFIG_FILE}" >&2
  echo "  Mount an existing config file into the container, e.g.:" >&2
  echo "    -v \$PWD/config.json:${PICOCLAW_HOME}/config.json:ro" >&2
  echo "  (or set PICOCLAW_CONFIG to the path of a mounted config)." >&2
  echo "  The container runs as uid/gid 1000: the file must be readable by it." >&2
  echo "  'picoclaw onboard' is NOT run: it is interactive and would hang." >&2
  exit "${EX_CONFIG}"
fi

# 3. Telegram token is required by THIS deployment (not by picoclaw itself).
if [ -z "${PICOCLAW_CHANNELS_TELEGRAM_TOKEN:-}" ]; then
  echo "ERROR: PICOCLAW_CHANNELS_TELEGRAM_TOKEN is not set or is empty." >&2
  echo "  This deployment (Telegram) requires it; picoclaw itself does not." >&2
  echo "  Pass the Telegram bot token as an environment variable, e.g.:" >&2
  echo "    docker run -e PICOCLAW_CHANNELS_TELEGRAM_TOKEN=<token> ..." >&2
  exit "${EX_CONFIG}"
fi

case "${PICOCLAW_CHANNELS_TELEGRAM_TOKEN}" in
  *[![:space:]]*) ;;
  *)
    echo "ERROR: PICOCLAW_CHANNELS_TELEGRAM_TOKEN is whitespace only." >&2
    echo "  This deployment (Telegram) requires a real bot token;" >&2
    echo "  picoclaw itself does not require one." >&2
    exit "${EX_CONFIG}"
    ;;
esac

# 4. The PID file is intentionally left alone.
# The gateway deals with stale PID files itself (pkg/pid/pidfile.go): it
# treats a recorded PID 1 as stale, checks whether the recorded process is
# still alive and verifies it really is picoclaw (/proc/<pid>/comm) before
# removing the file. Deleting it here would defeat that singleton check and
# allow two gateways to run on the same state directory.

# 5. Fail early with a clear message if the image is broken.
if ! command -v picoclaw >/dev/null 2>&1; then
  echo "ERROR: picoclaw binary not found in PATH" >&2
  exit "${EX_CONFIG}"
fi

# 6. exec is mandatory: it replaces the shell so SIGTERM from `docker stop`
# reaches the gateway directly and shutdown is clean.
exec picoclaw gateway --allow-empty "$@"
