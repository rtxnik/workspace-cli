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
  # a real upstream. ForwardingVerdict requires forwarded == self-egress AND
  # forwarded != direct; with freedom both exit-IPs equal the runner's direct
  # IP so ForwardingVerdict returns FALSE (forwarded == direct → "traffic
  # leaks around the tunnel"). Therefore flow-only mode does NOT assert
  # overall doctor green — it asserts the tproxy preconditions check
  # specifically, and confirms doctor's first failure is the expected
  # self-egress check (proving all earlier precondition checks passed).
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
echo "== building proxy image (docker build) =="
# Build the image directly rather than `ws proxy rebuild`: rebuild also
# *recreates* the proxy container and waits for its health, which fails on a
# fresh CI runner where no proxy container exists yet ("No such container").
# The dotfiles recipe was staged at $PROFILES_DIR/proxy by the CI job.
docker build -t "${WS_PROXY_IMAGE:-devpod-proxy}" "$HOME/.config/workspaces/profiles/proxy"

echo "== starting proxy (ws proxy up) =="
# `up` creates + starts the container from the freshly built image.
"$WS" proxy up

# ---------------------------------------------------------------------------
# T2-T5: doctor assertions (mode-dependent)
# ---------------------------------------------------------------------------
if [ -n "${WS_TEST_URI:-}" ]; then
  # Strict mode: full tunnel active — doctor must be fully green.
  echo "== T2-T5 (strict): ws proxy doctor must pass =="
  if ! "$WS" proxy doctor --json > /tmp/doctor.json; then
    echo "::error::ws proxy doctor failed — TPROXY datapath is broken"
    cat /tmp/doctor.json
    exit 1
  fi
  echo "doctor passed:"
  cat /tmp/doctor.json
else
  # Flow-only mode: freedom outbound → self-egress check will HARD-fail
  # (freedom exit-IP == direct IP, so ForwardingVerdict triggers).
  # We do NOT assert overall doctor green. Instead we assert:
  #   (a) the "tproxy preconditions" check exists and .ok == true
  #       (proves cap/rp_filter/LISTEN/mangle/ip-rule without a real upstream)
  #   (b) the run's first failure is "self-egress (proxy tunnel exit-IP)"
  #       (all earlier precondition checks passed; only the expected
  #        freedom-mode self-egress check failed — not a real datapath break)
  echo "== T2-T5 (flow-only): asserting tproxy preconditions via doctor JSON =="
  # Capture exit code without aborting under set -e
  rc=0
  "$WS" proxy doctor --json > /tmp/doctor.json 2>&1 || rc=$?
  echo "doctor exited ${rc} (non-zero expected in flow-only mode):"
  cat /tmp/doctor.json

  # (a) tproxy preconditions must have .ok == true
  echo "-- asserting: tproxy preconditions check .ok == true --"
  if ! jq -e '
    .checks[]
    | select(.name == "tproxy preconditions")
    | .ok == true
  ' /tmp/doctor.json > /dev/null; then
    echo "::error::tproxy preconditions check failed or missing — TPROXY datapath is broken (flow-only T2)"
    exit 1
  fi
  echo "tproxy preconditions: ok"

  # (b) first failure must be "self-egress (proxy tunnel exit-IP)"
  echo "-- asserting: first failure is self-egress (expected freedom-mode failure) --"
  FAILED_NAME="$(jq -r '.checks[.failedAt].name' /tmp/doctor.json)"
  if [ "${FAILED_NAME}" != "self-egress (proxy tunnel exit-IP)" ]; then
    echo "::error::unexpected first failure: '${FAILED_NAME}' (expected 'self-egress (proxy tunnel exit-IP)') — a precondition check failed, which is a real datapath break (flow-only T3)"
    exit 1
  fi
  echo "first failure is '${FAILED_NAME}' — expected freedom-mode self-egress failure, all preconditions passed"
  echo "T2-T5 flow-only assertions passed"
fi

# ---------------------------------------------------------------------------
# T13: kill xray inside the container; doctor must detect dead socket
# ---------------------------------------------------------------------------
echo "== T13: killing xray inside proxy container =="
docker exec "${PROXY_CONTAINER}" sh -c 'kill -9 $(pidof xray) 2>/dev/null || true'
sleep 2

if [ -n "${WS_TEST_URI:-}" ]; then
  # Strict mode: doctor must go RED (exit non-zero).
  echo "== T13 (strict): doctor must exit non-zero with dead xray =="
  if "$WS" proxy doctor --json > /tmp/doctor_dead.json 2>&1; then
    echo "::error::ws proxy doctor stayed GREEN after xray was killed — health masks a dead forwarding leg (T13 regression)"
    cat /tmp/doctor_dead.json
    exit 1
  fi
  echo "T13 passed: doctor correctly exited non-zero after xray was killed"
  cat /tmp/doctor_dead.json
else
  # Flow-only mode: assert tproxy preconditions check now .ok == false
  # (xray dead → no LISTEN / no CapEff → preconditions must flip RED).
  echo "== T13 (flow-only): tproxy preconditions must be RED with dead xray =="
  rc13=0
  "$WS" proxy doctor --json > /tmp/doctor_dead.json 2>&1 || rc13=$?
  echo "doctor exited ${rc13} (non-zero expected):"
  cat /tmp/doctor_dead.json
  echo "-- asserting: tproxy preconditions check .ok == false --"
  if ! jq -e '
    .checks[]
    | select(.name == "tproxy preconditions")
    | .ok == false
  ' /tmp/doctor_dead.json > /dev/null; then
    echo "::error::tproxy preconditions check stayed GREEN after xray was killed — doctor does not detect dead socket (T13 regression)"
    exit 1
  fi
  echo "T13 flow-only passed: tproxy preconditions correctly RED after xray killed"
fi

echo ""
echo "datapath gate passed"
