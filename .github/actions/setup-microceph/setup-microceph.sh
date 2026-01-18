# install_microceph: install MicroCeph from the specified channel
install_microceph() {
  local channel="${1}"
  # SNAP_CACHE_DIR is expected to be in the environment if set

  if [ -e test/includes/snap.sh ]; then
    sudo bash -c "
      . test/includes/snap.sh
      install_snap snapd latest/beta
      install_snap core24 latest/stable
      install_snap microceph \"${channel}\""
  else
    snap install microceph --channel="${channel}"
  fi
}

# configure_microceph: configure MicroCeph with the specified disk partitioned for the OSD count
configure_microceph() {
  local disk="${1}"
  local osd_count="${2}"

  sudo microceph cluster bootstrap
  sudo microceph.ceph config set global mon_allow_pool_size_one true
  sudo microceph.ceph config set global mon_allow_pool_delete true
  sudo microceph.ceph config set global osd_pool_default_size 1
  sudo microceph.ceph config set global osd_memory_target 939524096
  sudo microceph.ceph osd crush rule rm replicated_rule
  sudo microceph.ceph osd crush rule create-replicated replicated default osd
  for flag in nosnaptrim nobackfill norebalance norecover noscrub nodeep-scrub; do
    sudo microceph.ceph osd set "${flag}"
  done

  # If there is more than one OSD, set up partitions.
  if [ "${osd_count}" -gt 1 ]; then
    sudo blkdiscard "${disk}" --force
    sudo parted "${disk}" --script mklabel gpt

    for i in $(seq 1 "${osd_count}"); do
        # Create equal sized partitions for each OSD.
        min="$(( (i-1) *  100 / osd_count ))"
        max="$(( i * 100 / osd_count ))"
        sudo parted "${disk}" --align optimal --script mkpart primary "${min}%" "${max}%"
    done

    # Force the detection of the new partitions
    sudo partx --update "${disk}"

    # Allow (more) time for the kernel to pick up the new partitions
    disk_name="$(basename "${disk}")"
    for _ in 1 2 3; do
      parts="$(grep -cwE "${disk_name}[0-9]+$" /proc/partitions)"
      [ "${parts}" -ge "${osd_count}" ] && break
      sleep 1
    done

    for i in $(seq 1 "${osd_count}"); do
      # MicroCeph does not accept partitions directly.
      # See: https://github.com/canonical/microceph/issues/251
      disk_part="$(sudo losetup --find --nooverlap --direct-io=on --show "${disk}${i}")"

      # Retry logic for "microceph disk add" that can fail due to partitions not being ready
      # Error: unable to list system disks: Failed to find "/dev/disk/by-id/scsi-36...9e-part1": lstat /dev/disk/by-id/scsi-36...9e-part1: no such file or directory
      wipe=""
      for attempt in 1 2 3; do
        if sudo microceph disk add "${disk_part}" ${wipe}; then
          break # Success, exit retry loop
        elif [ "${attempt}" -lt 3 ]; then
          echo "WARN: \"microceph disk add ${disk_part}\" failed, retrying with \"--wipe\" (${attempt}/3)"
          # Clear any leftover data on the disk when retrying
          wipe="--wipe"
          sleep 1
        else
          echo "FAIL: \"microceph disk add ${disk_part}\" failed ${attempt} times"
          exit 1
        fi
      done
    done
  else
    sudo microceph disk add --wipe "${disk}"
  fi

  sudo rm -f /snap/bin/rbd
  sudo rm -rf /etc/ceph
  sudo ln -s /var/snap/microceph/current/conf /etc/ceph

  sudo microceph enable rgw
  sudo microceph.ceph osd pool create cephfs_meta 32
  sudo microceph.ceph osd pool create cephfs_data 32
  sudo microceph.ceph fs new cephfs cephfs_meta cephfs_data
  sudo microceph.ceph fs ls
}

# install_ceph_common: install ceph-common package for ceph CLI tools
install_ceph_common() {
  sudo apt-get update
  sudo apt-get install --no-install-recommends -y ceph-common
  # reclaim some space
  sudo apt-get clean
}

# wait_for_microceph: wait until MicroCeph is ready
wait_for_microceph() {
  # Wait until there are no more "unknowns" pgs
  for _ in $(seq 60); do
      if sudo microceph.ceph pg stat | grep -wF unknown; then
          sleep 1
      else
          break
      fi
  done
  sudo microceph.ceph status
}

# setup_microceph: install and configure MicroCeph with the specified disk and OSD count
# If no disk is specified, defaults to /dev/disk/by-id/*-lxd--ephemeral
setup_microceph() {
    local disk="${1:-"/dev/disk/by-id/*-lxd--ephemeral"}"
    if [ -z "$disk" ]; then
        echo "Usage: setup_microceph <disk> [osd_count] [channel]"
        return 1
    fi
    local osd_count="${2:-1}"
    local channel="${3:-latest/edge}"

    install_microceph "${channel}"
    configure_microceph "${disk}" "${osd_count}"
    install_ceph_common
    wait_for_microceph
}

# If the script is being run directly, execute the specified command
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    cmd="${1:-}"
    case "${cmd}" in
        install-microceph)
            shift
            install_microceph "$@"
            ;;
        configure-microceph)
            shift
            configure_microceph "$@"
            ;;
        install-ceph-common)
            install_ceph_common
            ;;
        wait-for-microceph)
            wait_for_microceph
            ;;
        *)
            setup_microceph "$@"
            ;;
    esac
fi
