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
  # Provision the freedom config as a NAMED PROFILE exactly the way
  # `ws proxy init` lays it out: profiles/freedom.json + a RELATIVE symlink
  # config.json -> profiles/freedom.json. The doctor's "active profile valid"
  # check (checkActiveProfileValid -> ReadActiveProfileName) requires
  # cfg.XrayConfig to be a symlink into profiles/ — a plain config.json regular
  # file fails that check at index 2, BEFORE the self-egress check (index 7)
  # this gate asserts on. The relative symlink resolves both on the host (the
  # doctor) and inside the container's whole-directory /etc/xray/ bind mount.
  mkdir -p "$HOME/.config/xray/profiles"
  cat > "$HOME/.config/xray/profiles/freedom.json" <<'XRAY_EOF'
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
  ln -sf profiles/freedom.json "$HOME/.config/xray/config.json"
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

# On failure, surface bounded crash-loop diagnostics that localize WHERE startup
# died — every docker invocation is timeout-wrapped so this block can never hang
# (a prior `timeout N su ...` diagnostic orphaned xray and hung the job 8+ min).
# On success the healthy container is left running for the doctor assertions below.
if [ "$up_rc" -ne 0 ]; then
  IMG="${WS_PROXY_IMAGE:-devpod-proxy}"
  echo "== proxy container state =="
  docker ps -a --filter "name=${PROXY_CONTAINER}" --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' || true
  echo "== proxy container logs (bounded) =="
  timeout 15 docker logs "${PROXY_CONTAINER}" 2>&1 | tail -40 \
    || echo "(logs unavailable — container restarting; see bounded diagnostics below)"

  echo "== diag 1: file capability on the xray binary (did the setcap xattr survive into the runtime image?) =="
  timeout 30 docker run --rm --entrypoint getcap "$IMG" /usr/local/bin/xray 2>&1 | head -5 || true

  echo "== diag 2: one-shot REAL entrypoint, mirroring 'ws proxy up' HostConfig, --rm + no restart, bounded 20s =="
  echo "   (shows the exact line the entrypoint dies on: iptables / rp_filter / xray validate / xray exec)"
  d2=0
  timeout 20 docker run --rm \
    --cap-add NET_ADMIN \
    --sysctl net.ipv4.ip_forward=1 \
    --sysctl net.ipv4.conf.all.rp_filter=0 \
    --sysctl net.ipv4.conf.default.rp_filter=0 \
    --sysctl net.ipv4.conf.lo.rp_filter=0 \
    --sysctl net.ipv4.conf.all.route_localnet=1 \
    --sysctl net.ipv4.conf.default.route_localnet=1 \
    --sysctl net.ipv4.conf.lo.route_localnet=1 \
    -v "$HOME/.config/xray:/etc/xray:ro" \
    "$IMG" > /tmp/diag2.log 2>&1 || d2=$?
  echo "   diag2 exit: $d2 (124 = stayed up past 20s = healthy startup; fast non-zero = startup abort)"
  head -60 /tmp/diag2.log || true

  echo "== diag 3: xray config validation as the 'xray' user (catches config schema drift) =="
  timeout 30 docker run --rm --cap-add NET_ADMIN --user xray \
    -v "$HOME/.config/xray:/etc/xray:ro" \
    --entrypoint /usr/local/bin/xray "$IMG" run -test -c /etc/xray/config.json > /tmp/diag3.log 2>&1 || true
  head -30 /tmp/diag3.log || true

  echo "== diag 4: real xray run as 'xray' user, bounded 5s — isolates the IP_TRANSPARENT bind =="
  echo "   (exit 124 = bound the tproxy socket and stayed up = caps OK; fast non-124 exit = bind/cap failure)"
  d4=0
  timeout 5 docker run --rm --cap-add NET_ADMIN --user xray \
    -v "$HOME/.config/xray:/etc/xray:ro" \
    --entrypoint /usr/local/bin/xray "$IMG" run -c /etc/xray/config.json > /tmp/diag4.log 2>&1 || d4=$?
  echo "   diag4 exit: $d4 (124 = bind OK)"
  head -40 /tmp/diag4.log || true

  docker rm -f "${PROXY_CONTAINER}" >/dev/null 2>&1 || true
  echo "::error::ws proxy up failed (rc=$up_rc) — see bounded diagnostics above"
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
# Self-UDP capture probe: the proxy's OWN UDP egress must be MARKed by the
# mangle XRAY_SELF chain (the old nat REDIRECT was TCP-only → UDP self-egress
# leaked around the tunnel). Capture-only: we assert the UDP MARK rule's packet
# counter increments when the container (as root, NOT uid xray) emits one
# datagram to a PUBLIC dst — NOT a full round-trip (freedom has no UDP echo).
# ---------------------------------------------------------------------------
echo "== self-UDP capture probe: XRAY_SELF must MARK the proxy's own UDP egress =="
# Zero the chain counters for a deterministic reading.
docker exec "${PROXY_CONTAINER}" iptables -t mangle -Z XRAY_SELF \
  || { echo "::error::XRAY_SELF chain missing — self-egress contour was not applied by the entrypoint"; exit 1; }
# Emit exactly one locally-originated UDP datagram to a PUBLIC ip:port. bash ships
# in ubuntu:24.04; /dev/udp opens the socket and the write sends one datagram.
# The MARK happens during OUTPUT traversal, so external reachability is irrelevant.
docker exec "${PROXY_CONTAINER}" bash -c 'echo -n x > /dev/udp/8.8.8.8/53' || true
# Read the udp MARK rule's packet counter (col 1 = pkts) from the chain.
# `iptables -L -v -x -n` on the nft backend (ubuntu-latest) renders the protocol
# column NUMERICALLY (17 = udp, 6 = tcp), not as a name — so match both forms.
udp_pkts="$(docker exec "${PROXY_CONTAINER}" iptables -t mangle -L XRAY_SELF -v -x -n \
  | awk '$3 == "MARK" && ($4 == "udp" || $4 == "17") { print $1; exit }')"
echo "XRAY_SELF udp MARK rule matched ${udp_pkts:-0} packet(s)"
if [ -z "${udp_pkts:-}" ] || [ "${udp_pkts:-0}" -lt 1 ]; then
  echo "::error::self-UDP egress was NOT marked by mangle XRAY_SELF — UDP self-egress leaks around the tunnel (TCP-only REDIRECT regression)"
  docker exec "${PROXY_CONTAINER}" iptables -t mangle -L XRAY_SELF -v -x -n || true
  exit 1
fi
echo "self-UDP capture probe passed (the proxy's own UDP egress is marked into the tunnel)"

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
