#
test_snap_basic() {
  if [ "${LXD_VM_TESTS:-0}" = "0" ]; then
    export TEST_UNMET_REQUIREMENT="LXD_VM_TESTS=1 is required"
    return
  fi

  lxc launch ubuntu-minimal-daily:24.04 v1 --vm -c limits.memory=384MiB -d "${SMALL_VM_ROOT_DISK}"
  sleep 30

  echo "==> Check VM state transitions"
  lxc list
  [ "$(lxc list -f csv -c s)" = "RUNNING" ]
  lxc pause v1
  [ "$(lxc list -f csv -c s)" = "FROZEN" ]
  ! lxc stop v1 || false
  lxc start v1

  echo "==> Check exec operations"
  lxc exec v1 -- true
  ! lxc exec v1 -- false || false
  [ "$(lxc exec v1 -- hostname)" = "v1" ]

  lxc stop -f v1
  [ "$(lxc list -f csv -c s)" = "STOPPED" ]

  lxc delete v1
}
