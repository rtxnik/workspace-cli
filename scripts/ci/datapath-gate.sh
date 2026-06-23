#!/usr/bin/env bash
# Red-line TPROXY datapath gate (T2-T5 + T13).
# Runs in CI (privileged, Ubuntu runner with xt_TPROXY support).
# Cannot be run locally — requires a privileged runner and a built proxy image.
#
# Environment:
#   WS_BIN        path to the ws binary (default: ./ws)
#   WS_TEST_URI   VLESS or Hysteria2 URI (repo secret WS_TEST_ENDPOINT).
#                 When set: provisions a real profile and asserts full doctor pass
#                 including exit-IP comparison (strict mode).
#                 When unset: flow-only mode — doctor asserts preconditions and
#                 forwarding leg but SKIPS exit-IP comparison (see loud echo below).
#   WS_PROXY_CONTAINER  proxy container name (default: dev-proxy)
set -euo pipefail

WS="${WS_BIN:-./ws}"
PROXY_CONTAINER="${WS_PROXY_CONTAINER:-dev-proxy}"

# ---------------------------------------------------------------------------
# Preflight: TPROXY kernel modules
# ---------------------------------------------------------------------------
echo "== preflight: TPROXY kernel modules =="
if ! sudo modprobe xt_TPROXY xt_socket; then
  echo "::error::runner kernel lacks xt_TPROXY/xt_socket; datapath gate cannot run"
  exit 1
fi

# ---------------------------------------------------------------------------
# Profile / config provisioning
# ---------------------------------------------------------------------------
# The dotfiles recipe (staged by the CI job at $HOME/.config/workspaces/profiles/proxy)
# provides the Dockerfile for ws proxy rebuild. The xray config is separate:
#   - Strict mode (WS_TEST_URI set): provision from the real URI via ws proxy init.
#   - Flow-only mode (WS_TEST_URI unset): write a minimal freedom-outbound config
#     so the container starts and TPROXY preconditions can be tested; exit-IP
#     comparison is skipped (forwarding datapath is still asserted).
if [ -z "${WS_TEST_URI:-}" ]; then
  echo ""
  echo "======================================================================="
  echo "WARNING: WS_TEST_URI is not set."
  echo "Running in FLOW-ONLY mode: TPROXY preconditions + forwarding datapath"
  echo "are asserted but exit-IP comparison is SKIPPED."
  echo "To enable the full gate, set the WS_TEST_ENDPOINT repo secret."
  echo "======================================================================="
  echo ""

  # Write a minimal xray config with a freedom outbound so the container
  # can start and the TPROXY socket / forwarding leg can be tested without
  # a real upstream. The forwarding probe runs a sidecar curl; with freedom
  # its exit-IP equals the runner's direct IP (ForwardingVerdict accepts any
  # non-zero forwarded IP when direct == forwarded because no tunnel is
  # active — the precondition + forwarding-leg bind are still exercised).
  mkdir -p "$HOME/.config/xray"
  cat > "$HOME/.config/xray/config.json" <<'XRAY_EOF'
{
  "inbounds": [
    {
      "port": 12345,
      "protocol": "dokodemo-door",
      "settings": {"network": "tcp,udp", "followRedirect": true},
      "streamSettings": {"sockopt": {"tproxy": "tproxy"}}
    }
  ],
  "outbounds": [
    {"protocol": "freedom", "settings": {}}
  ]
}
XRAY_EOF
else
  echo "== strict mode: provisioning profile from WS_TEST_URI =="
  # ws proxy init <uri> parses VLESS or Hysteria2 URIs and writes
  # ~/.config/xray/config.json (or cfg.XrayConfig).
  "$WS" proxy init "${WS_TEST_URI}"
fi

# ---------------------------------------------------------------------------
# Build image and bring proxy up
# ---------------------------------------------------------------------------
echo "== building proxy image (ws proxy rebuild) =="
# ws proxy rebuild runs docker build against $PROFILES_DIR/proxy.
# The dotfiles recipe must already be staged there by the CI job.
"$WS" proxy rebuild --force

echo "== starting proxy (ws proxy up) =="
"$WS" proxy up

# ---------------------------------------------------------------------------
# T2-T5: doctor must be fully green
# ---------------------------------------------------------------------------
echo "== T2-T5: ws proxy doctor must pass =="
if ! "$WS" proxy doctor --json > /tmp/doctor.json; then
  echo "::error::ws proxy doctor failed — TPROXY datapath is broken"
  cat /tmp/doctor.json
  exit 1
fi
echo "doctor passed:"
cat /tmp/doctor.json

# ---------------------------------------------------------------------------
# T13: kill xray inside the container; doctor must go RED
# This asserts that doctor does NOT mask a dead forwarding socket.
# ---------------------------------------------------------------------------
echo "== T13: killing xray inside proxy container =="
docker exec "${PROXY_CONTAINER}" sh -c 'kill -9 $(pidof xray) 2>/dev/null || true'
sleep 2

echo "== T13: doctor must exit non-zero with dead xray =="
if "$WS" proxy doctor --json > /tmp/doctor_dead.json 2>&1; then
  echo "::error::ws proxy doctor stayed GREEN after xray was killed — health masks a dead forwarding leg (T13 regression)"
  cat /tmp/doctor_dead.json
  exit 1
fi
echo "T13 passed: doctor correctly exited non-zero after xray was killed"
cat /tmp/doctor_dead.json

echo ""
echo "datapath gate passed"
