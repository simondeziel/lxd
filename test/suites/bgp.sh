test_bgp() {
  ensure_has_localhost_remote "${LXD_ADDR}"

  local BGP_ADDR="127.0.0.1"
  local BGP_PORT
  BGP_PORT="$(local_tcp_port)"
  local BGP_PORT_ALT
  BGP_PORT_ALT="$(local_tcp_port)"
  local BGP_ROUTER_ID="192.0.2.1"
  local BGP_ASN="65000"

  sub_test "Configure BGP listener and verify it is listening on the expected address and port"
  lxc config set core.bgp_address="${BGP_ADDR}:${BGP_PORT}" core.bgp_routerid="${BGP_ROUTER_ID}" core.bgp_asn="${BGP_ASN}"

  # Wait for the BGP listener to come up.
  for _ in $(seq 10); do
    nc -z "${BGP_ADDR}" "${BGP_PORT}" 2>/dev/null && break
    sleep 0.1
  done

  if ! nc -z "${BGP_ADDR}" "${BGP_PORT}" 2>/dev/null; then
    echo "ERROR: BGP listener did not come up on ${BGP_ADDR}:${BGP_PORT}, aborting" >&2
    exit 1
  fi

  sub_test "Reconfigure BGP listener to a different port and verify it follows"
  lxc config set core.bgp_address "${BGP_ADDR}:${BGP_PORT_ALT}"

  # Allow time for the old listener to stop and the new one to start.
  for _ in $(seq 10); do
    nc -z "${BGP_ADDR}" "${BGP_PORT_ALT}" 2>/dev/null && break
    sleep 0.1
  done

  if ! nc -z "${BGP_ADDR}" "${BGP_PORT_ALT}" 2>/dev/null; then
    echo "ERROR: BGP listener did not come up on ${BGP_ADDR}:${BGP_PORT_ALT} after reconfiguration, aborting" >&2
    exit 1
  fi

  # Wait for the old listener to stop (shutdown is asynchronous).
  for _ in $(seq 10); do
    nc -z "${BGP_ADDR}" "${BGP_PORT}" 2>/dev/null || break
    sleep 0.1
  done

  # The old port must no longer be in use.
  if nc -z "${BGP_ADDR}" "${BGP_PORT}" 2>/dev/null; then
    echo "ERROR: BGP listener is still up on old port ${BGP_PORT} after reconfiguration, aborting" >&2
    exit 1
  fi

  sub_test "Unconfigure BGP listener and verify it is no longer listening"
  lxc config set core.bgp_address="" core.bgp_routerid="" core.bgp_asn=""

  for _ in $(seq 10); do
    nc -z "${BGP_ADDR}" "${BGP_PORT_ALT}" 2>/dev/null || break
    sleep 0.1
  done

  if nc -z "${BGP_ADDR}" "${BGP_PORT_ALT}" 2>/dev/null; then
    echo "ERROR: BGP listener is still up on ${BGP_ADDR}:${BGP_PORT_ALT} after unconfiguration, aborting" >&2
    exit 1
  fi

  sub_test "Verify core.bgp_peers_gtsm defaults to false"
  [ "$(lxc config get core.bgp_peers_gtsm)" = "false" ]

  sub_test "Enable GTSM and verify the BGP listener remains reachable"
  lxc config set core.bgp_address "${BGP_ADDR}:${BGP_PORT}" core.bgp_routerid "${BGP_ROUTER_ID}" core.bgp_asn "${BGP_ASN}"
  for _ in $(seq 10); do
    bgp_marker_check "${BGP_ADDR}" "${BGP_PORT}" && break
    sleep 1
  done
  if ! bgp_marker_check "${BGP_ADDR}" "${BGP_PORT}"; then
    echo "ERROR: BGP listener did not come up before enabling GTSM, aborting" >&2
    exit 1
  fi

  lxc config set core.bgp_peers_gtsm true
  [ "$(lxc config get core.bgp_peers_gtsm)" = "true" ]
  # The listener must survive the GTSM reconfiguration.
  if ! bgp_marker_check "${BGP_ADDR}" "${BGP_PORT}"; then
    echo "ERROR: BGP listener stopped responding after enabling GTSM, aborting" >&2
    exit 1
  fi

  sub_test "Disable GTSM and verify the BGP listener remains reachable"
  lxc config set core.bgp_peers_gtsm false
  [ "$(lxc config get core.bgp_peers_gtsm)" = "false" ]
  if ! bgp_marker_check "${BGP_ADDR}" "${BGP_PORT}"; then
    echo "ERROR: BGP listener stopped responding after disabling GTSM, aborting" >&2
    exit 1
  fi

  # Cleanup.
  lxc config unset core.bgp_address core.bgp_routerid core.bgp_asn core.bgp_peers_gtsm
}
