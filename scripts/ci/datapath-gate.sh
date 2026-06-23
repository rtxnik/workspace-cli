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
up_rc=0
"$WS" proxy up || up_rc=$?

# Always surface container state — the entrypoint/xray crash loop is the ground
# truth for why the datapath did or did not come up.
echo "== proxy container state =="
docker ps -a --filter "name=${PROXY_CONTAINER}" --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' || true
echo "== proxy container logs (bounded) =="
timeout 15 docker logs "${PROXY_CONTAINER}" 2>&1 | tail -40 \
  || echo "(logs unavailable — container restarting; see bounded diagnostics below)"

# ---------------------------------------------------------------------------
# Bounded crash-loop diagnostics. EVERY docker invocation is timeout-wrapped so
# this block can never hang (a prior `timeout N su ...` diagnostic orphaned xray
# and hung the job for 8+ min). Goal: capture WHERE startup dies — iptables, an
# rp_filter write, xray config validation, or the IP_TRANSPARENT bind — without
# a restart loop masking the stderr.
# ---------------------------------------------------------------------------
IMG="${WS_PROXY_IMAGE:-devpod-proxy}"

echo "== diag 1: file capability on the xray binary (did the setcap xattr survive into the runtime image?) =="
timeout 30 docker run --rm --entrypoint getcap "$IMG" /usr/local/bin/xray 2>&1 | head -5 || true

echo "== diag 2: one-shot REAL entrypoint, mirroring 'ws proxy up' HostConfig (cap-add + sysctls), --rm + no restart, bounded 20s =="
echo "   (this shows the exact line the entrypoint dies on: iptables / rp_filter / xray validate / xray exec)"
d2=0
timeout 20 docker run --rm \
  --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=1 \
  --sysctl net.ipv4.conf.all.rp_filter=0 \
  --sysctl net.ipv4.conf.default.rp_filter=0 \
  --sysctl net.ipv4.conf.all.route_localnet=1 \
  -v "$HOME/.config/xray:/etc/xray:ro" \
  "$IMG" > /tmp/diag2.log 2>&1 || d2=$?
echo "   diag2 exit: $d2 (124 = stayed up past 20s = healthy startup; fast non-zero = startup abort)"
head -60 /tmp/diag2.log || true

echo "== diag 3: xray config validation as the 'xray' user (catches v26.2.6 schema drift / candidate b) =="
timeout 30 docker run --rm --cap-add NET_ADMIN --user xray \
  -v "$HOME/.config/xray:/etc/xray:ro" \
  --entrypoint /usr/local/bin/xray "$IMG" run -test -c /etc/xray/config.json > /tmp/diag3.log 2>&1 || true
head -30 /tmp/diag3.log || true

echo "== diag 4: real xray run as 'xray' user (non-root, file-cap path), bounded 5s — isolates the IP_TRANSPARENT bind (candidate a) =="
echo "   (exit 124 = bound the tproxy socket and stayed up = caps OK; fast non-124 exit with an error = bind/cap failure)"
d4=0
timeout 5 docker run --rm --cap-add NET_ADMIN --user xray \
  -v "$HOME/.config/xray:/etc/xray:ro" \
  --entrypoint /usr/local/bin/xray "$IMG" run -c /etc/xray/config.json > /tmp/diag4.log 2>&1 || d4=$?
echo "   diag4 exit: $d4 (124 = bind OK)"
head -40 /tmp/diag4.log || true

echo "== diag 5: sysctl values + writability (root cause is EROFS on /proc/sys at entrypoint line 28) =="
echo "-- (A) with the 'ws proxy up' HostConfig --sysctl set: what every iface INHERITS --"
# shellcheck disable=SC2016  # $(...) is meant to run inside the container, not expand on the host
timeout 30 docker run --rm \
  --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=1 \
  --sysctl net.ipv4.conf.all.rp_filter=0 \
  --sysctl net.ipv4.conf.default.rp_filter=0 \
  --sysctl net.ipv4.conf.all.route_localnet=1 \
  --entrypoint sh "$IMG" -c '
    echo "rp_filter:";      grep -H . /proc/sys/net/ipv4/conf/*/rp_filter
    echo "route_localnet:"; grep -H . /proc/sys/net/ipv4/conf/*/route_localnet
    echo "ip_forward: $(cat /proc/sys/net/ipv4/ip_forward)"
    if echo 0 > /proc/sys/net/ipv4/conf/all/rp_filter 2>/dev/null; then echo "proc-sys: WRITABLE"; else echo "proc-sys: READ-ONLY (EROFS) -- confirms root cause"; fi
  ' 2>&1 | head -40 || true
echo "-- (B) plain container, NO --sysctl: fresh-netns kernel defaults --"
timeout 30 docker run --rm --entrypoint sh "$IMG" -c '
    echo "rp_filter:";      grep -H . /proc/sys/net/ipv4/conf/*/rp_filter
    echo "route_localnet:"; grep -H . /proc/sys/net/ipv4/conf/*/route_localnet
  ' 2>&1 | head -20 || true

# Drop the restart-looping container so it cannot keep flapping during later steps.
docker rm -f "${PROXY_CONTAINER}" >/dev/null 2>&1 || true

if [ "$up_rc" -ne 0 ]; then
  echo "::error::ws proxy up failed (rc=$up_rc) — see container logs / direct run above"
  exit 1
fi

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
