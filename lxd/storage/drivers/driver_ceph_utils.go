package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/ioprogress"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/units"
)

// cephBlockVolSuffix suffix used for block content type volumes.
const cephBlockVolSuffix = ".block"

// cephISOVolSuffix suffix used for iso content type volumes.
const cephISOVolSuffix = ".iso"

const cephVolumeTypeZombieImage = VolumeType("zombie_image")

// CephDefaultCluster represents the default ceph cluster name.
const CephDefaultCluster = "ceph"

// CephDefaultUser represents the default ceph user name.
const CephDefaultUser = "admin"

// cephVolTypePrefixes maps volume type to storage volume name prefix.
var cephVolTypePrefixes = map[VolumeType]string{
	VolumeTypeContainer: cluster.StoragePoolVolumeTypeNameContainer,
	VolumeTypeVM:        cluster.StoragePoolVolumeTypeNameVM,
	VolumeTypeImage:     cluster.StoragePoolVolumeTypeNameImage,
	VolumeTypeCustom:    cluster.StoragePoolVolumeTypeNameCustom,
}

// osdPoolExists checks whether a given OSD pool exists.
func (d *ceph) osdPoolExists() (bool, error) {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"ceph",
		"--name", "client."+d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"osd",
		"pool",
		"get",
		d.config["ceph.osd.pool_name"],
		"size")

	if err != nil {
		status, _ := shared.ExitStatus(err)
		// If the error status code is 2, the pool definitely doesn't exist.
		if status == 2 {
			return false, nil
		}

		// Else, the error status is not 0 or 2,
		// so we can't be sure if the pool exists or not
		// as it might be a network issue, an internal ceph issue, etc.
		return false, err
	}

	return true, nil
}

// osdDeletePool destroys an OSD pool.
//   - A call to osdDeletePool will destroy a pool including any storage
//     volumes that still exist in the pool.
//   - In case the OSD pool that is supposed to be deleted does not exist this
//     command will still exit 0. This means that if the caller wants to be sure
//     that this call actually deleted an OSD pool it needs to check for the
//     existence of the pool first.
func (d *ceph) osdDeletePool() error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"ceph",
		"--name", "client."+d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"osd",
		"pool",
		"delete",
		d.config["ceph.osd.pool_name"],
		d.config["ceph.osd.pool_name"],
		"--yes-i-really-really-mean-it")
	if err != nil {
		return err
	}

	return nil
}

func (d *ceph) roundUpTo512(a int64) int64 {
	result := (a / 512) * 512
	if a%512 != 0 {
		result += 512
	}

	return result
}

// rbdCreateVolume creates an RBD storage volume.
// Note that the default set of features is intentionally limited
// by passing --image-feature explicitly. This is done to ensure that
// the chances of a conflict between the features supported by the userspace
// library and the kernel module are minimized. Otherwise random panics might
// occur.
func (d *ceph) rbdCreateVolume(vol Volume, size string) error {
	sizeBytes, err := units.ParseByteSizeString(size)
	if err != nil {
		return err
	}

	cmd := []string{
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
	}

	if d.config["ceph.rbd.features"] != "" {
		for _, feature := range shared.SplitNTrimSpace(d.config["ceph.rbd.features"], ",", -1, true) {
			cmd = append(cmd, "--image-feature", feature)
		}
	} else {
		cmd = append(cmd, "--image-feature", "layering")
	}

	if d.config["ceph.osd.data_pool_name"] != "" {
		cmd = append(cmd, "--data-pool", d.config["ceph.osd.data_pool_name"])
	}

	// Ceph allows writing only to images of size in multiples of 512B
	sizeBytes = d.roundUpTo512(sizeBytes)

	cmd = append(cmd,
		"--size", fmt.Sprintf("%dB", sizeBytes),
		"create",
		d.getRBDVolumeName(vol, "", false))

	_, err = shared.RunCommandContext(context.TODO(), "rbd", cmd...)
	return err
}

// rbdDeleteVolume deletes an RBD storage volume.
//   - In case the RBD storage volume that is supposed to be deleted does not
//     exist this command will still exit 0. This means that if the caller wants
//     to be sure that this call actually deleted an RBD storage volume it needs
//     to check for the existence of the pool first.
func (d *ceph) rbdDeleteVolume(vol Volume) error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"rm",
		d.getRBDVolumeName(vol, "", false))
	if err != nil {
		return err
	}

	return nil
}

// rbdMapVolume maps a given RBD storage volume.
// This will ensure that the RBD storage volume is accessible as a block device
// in the /dev directory and is therefore necessary in order to mount it.
func (d *ceph) rbdMapVolume(vol Volume) (string, error) {
	rbdName := d.getRBDVolumeName(vol, "", false)
	devPath, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"map",
		rbdName)
	if err != nil {
		return "", err
	}

	idx := strings.Index(devPath, "/dev/rbd")
	if idx < 0 {
		return "", errors.New("Failed to detect mapped device path")
	}

	devPath = strings.TrimSpace(devPath[idx:])

	d.logger.Debug("Activated RBD volume", logger.Ctx{"volName": rbdName, "dev": devPath})
	return devPath, nil
}

// rbdUnmapVolume unmaps a given RBD storage volume.
// This is a precondition in order to delete an RBD storage volume can.
func (d *ceph) rbdUnmapVolume(vol Volume, unmapUntilEINVAL bool) error {
	busyCount := 0
	rbdVol := d.getRBDVolumeName(vol, "", false)

	ourDeactivate := false

again:
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"unmap",
		rbdVol)
	if err != nil {
		runError, ok := err.(shared.RunError)
		if ok {
			exitError, ok := runError.Unwrap().(*exec.ExitError)
			if ok {
				if exitError.ExitCode() == 22 {
					// EINVAL (already unmapped).
					if ourDeactivate {
						d.logger.Debug("Deactivated RBD volume", logger.Ctx{"volName": rbdVol})
					}

					return nil
				}

				if exitError.ExitCode() == 16 {
					// EBUSY (currently in use).
					busyCount++
					if busyCount == 10 {
						return err
					}

					// Wait a second an try again.
					time.Sleep(time.Second)
					goto again
				}
			}
		}

		return err
	}

	if unmapUntilEINVAL {
		ourDeactivate = true
		goto again
	}

	d.logger.Debug("Deactivated RBD volume", logger.Ctx{"volName": rbdVol})

	return nil
}

// rbdUnmapVolumeSnapshot unmaps a given RBD snapshot.
// This is a precondition in order to delete an RBD snapshot can.
func (d *ceph) rbdUnmapVolumeSnapshot(vol Volume, snapshotName string, unmapUntilEINVAL bool) error {
again:
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"unmap",
		d.getRBDVolumeName(vol, snapshotName, false))
	if err != nil {
		runError, ok := err.(shared.RunError)
		if ok {
			exitError, ok := runError.Unwrap().(*exec.ExitError)
			if ok {
				if exitError.ExitCode() == 22 {
					// EINVAL (already unmapped).
					return nil
				}
			}
		}

		return err
	}

	if unmapUntilEINVAL {
		goto again
	}

	return nil
}

// rbdCreateVolumeSnapshot creates a read-write snapshot of a given RBD storage volume.
func (d *ceph) rbdCreateVolumeSnapshot(vol Volume, snapshotName string) error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"snap",
		"create",
		"--snap", snapshotName,
		d.getRBDVolumeName(vol, "", false))
	if err != nil {
		return err
	}

	return nil
}

// rbdProtectVolumeSnapshot protects a given snapshot from being deleted.
// This is a precondition to be able to create RBD clones from a given snapshot.
func (d *ceph) rbdProtectVolumeSnapshot(vol Volume, snapshotName string) error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"snap",
		"protect",
		"--snap", snapshotName,
		d.getRBDVolumeName(vol, "", false))
	if err != nil {
		runError, ok := err.(shared.RunError)
		if ok {
			exitError, ok := runError.Unwrap().(*exec.ExitError)
			if ok {
				if exitError.ExitCode() == 16 {
					// EBUSY (snapshot already protected).
					return nil
				}
			}
		}

		return err
	}

	return nil
}

// rbdUnprotectVolumeSnapshot unprotects a given snapshot.
// - This is a precondition to be able to delete an RBD snapshot.
// - This command will only succeed if the snapshot does not have any clones.
func (d *ceph) rbdUnprotectVolumeSnapshot(vol Volume, snapshotName string) error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"snap",
		"unprotect",
		"--snap", snapshotName,
		d.getRBDVolumeName(vol, "", false))
	if err != nil {
		runError, ok := err.(shared.RunError)
		if ok {
			exitError, ok := runError.Unwrap().(*exec.ExitError)
			if ok {
				if exitError.ExitCode() == 22 {
					// EBUSY (snapshot already unprotected).
					return nil
				}
			}
		}

		return err
	}

	return nil
}

// rbdCreateClone creates a clone from a protected RBD snapshot.
func (d *ceph) rbdCreateClone(sourceVol Volume, sourceSnapshotName string, targetVol Volume) error {
	cmd := []string{
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
	}

	if d.config["ceph.rbd.features"] != "" {
		for _, feature := range shared.SplitNTrimSpace(d.config["ceph.rbd.features"], ",", -1, true) {
			cmd = append(cmd, "--image-feature", feature)
		}
	} else {
		cmd = append(cmd, "--image-feature", "layering")
	}

	if d.config["ceph.osd.data_pool_name"] != "" {
		cmd = append(cmd, "--data-pool", d.config["ceph.osd.data_pool_name"])
	}

	cmd = append(cmd,
		"clone",
		d.getRBDVolumeName(sourceVol, sourceSnapshotName, true),
		d.getRBDVolumeName(targetVol, "", true))

	_, err := shared.RunCommandContext(context.TODO(), "rbd", cmd...)
	if err != nil {
		return err
	}

	return nil
}

// rbdListSnapshotClones list all clones of an RBD snapshot.
func (d *ceph) rbdListSnapshotClones(vol Volume, snapshotName string) ([]string, error) {
	msg, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"children",
		"--image", d.getRBDVolumeName(vol, "", false),
		"--snap", snapshotName)
	if err != nil {
		return nil, err
	}

	msg = strings.TrimSpace(msg)
	clones := strings.Fields(msg)
	if len(clones) == 0 {
		return nil, api.StatusErrorf(http.StatusNotFound, "Ceph RBD volume snapshot not found")
	}

	return clones, nil
}

// rbdMarkVolumeDeleted marks an RBD storage volume as being in "zombie" state.
// An RBD storage volume that is in zombie state is not tracked in LXD's
// database anymore but still needs to be kept around for the sake of any
// dependent storage entities in the storage pool. This usually happens when an
// RBD storage volume has protected snapshots; a scenario most common when
// creating a sparse copy of a container or when LXD updated an image and the
// image still has dependent container clones.
func (d *ceph) rbdMarkVolumeDeleted(vol Volume, newVolumeName string) error {
	// Ensure that new volume contains the config from the source volume to maintain filesystem suffix on
	// new volume name generated in getRBDVolumeName.
	newVol := NewVolume(d, d.name, vol.volType, vol.contentType, newVolumeName, vol.config, vol.poolConfig)
	newVol.isDeleted = true
	deletedName := d.getRBDVolumeName(newVol, "", true)

	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"mv",
		d.getRBDVolumeName(vol, "", true),
		deletedName,
	)
	if err != nil {
		return err
	}

	return nil
}

// rbdRenameVolume renames a given RBD storage volume.
// Note that this usually requires that the image be unmapped under its original
// name, then renamed, and finally will be remapped again. If it is not unmapped
// under its original name and the callers maps it under its new name the image
// will be mapped twice. This will prevent it from being deleted.
func (d *ceph) rbdRenameVolume(vol Volume, newVolumeName string) error {
	// Ensure that new volume contains the config from the source volume to maintain filesystem suffix on
	// new volume name generated in getRBDVolumeName.
	newVol := NewVolume(d, d.name, vol.volType, vol.contentType, newVolumeName, vol.config, vol.poolConfig)

	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"mv",
		d.getRBDVolumeName(vol, "", true),
		d.getRBDVolumeName(newVol, "", true),
	)
	if err != nil {
		return err
	}

	return nil
}

// rbdRenameVolumeSnapshot renames a given RBD storage volume.
// Note that if the snapshot is mapped - which it usually shouldn't be - this
// usually requires that the snapshot be unmapped under its original name, then
// renamed, and finally will be remapped again. If it is not unmapped under its
// original name and the caller maps it under its new name the snapshot will be
// mapped twice. This will prevent it from being deleted.
func (d *ceph) rbdRenameVolumeSnapshot(vol Volume, oldSnapshotName string, newSnapshotName string) error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"snap",
		"rename",
		d.getRBDVolumeName(vol, oldSnapshotName, true),
		d.getRBDVolumeName(vol, newSnapshotName, true))
	if err != nil {
		return err
	}

	return nil
}

// rbdGetVolumeParent will return the snapshot the RBD clone was created from:
//   - If the RBD storage volume is not a clone then this function will return
//     db.NoSuchObjectError.
//   - The snapshot will be returned as
//     <osd-pool-name>/<rbd-volume-name>@<rbd-snapshot-name>
//     The caller will usually want to parse this according to its needs. This
//     helper library provides two small functions to do this but see below.
func (d *ceph) rbdGetVolumeParent(vol Volume) (string, error) {
	msg, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"info",
		d.getRBDVolumeName(vol, "", false))
	if err != nil {
		return "", err
	}

	idx := strings.Index(msg, "parent: ")
	if idx == -1 {
		return "", api.StatusErrorf(http.StatusNotFound, "Ceph RBD volume parent not found")
	}

	msg = msg[(idx + len("parent: ")):]
	msg = strings.TrimSpace(msg)

	idx = strings.Index(msg, "\n")
	if idx == -1 {
		return "", errors.New("Unexpected parsing error")
	}

	msg = msg[:idx]
	msg = strings.TrimSpace(msg)

	return msg, nil
}

// rbdDeleteVolumeSnapshot deletes an RBD snapshot.
// This requires that the snapshot does not have any clones and is unmapped and
// unprotected.
func (d *ceph) rbdDeleteVolumeSnapshot(vol Volume, snapshotName string) error {
	_, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"snap",
		"rm",
		d.getRBDVolumeName(vol, snapshotName, false))
	if err != nil {
		return err
	}

	return nil
}

// rbdListVolumeSnapshots retrieves the snapshots of an RBD storage volume.
// The format of the snapshot names is simply the part after the @. So given a
// valid RBD path relative to a pool
// <osd-pool-name>/<rbd-storage-volume>@<rbd-snapshot-name>
// this will only return
// <rbd-snapshot-name>.
func (d *ceph) rbdListVolumeSnapshots(vol Volume) ([]string, error) {
	msg, err := shared.RunCommandContext(
		context.TODO(),
		"rbd",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"--format", "json",
		"snap",
		"ls",
		d.getRBDVolumeName(vol, "", false))
	if err != nil {
		return []string{}, err
	}

	var data []map[string]any
	err = json.Unmarshal([]byte(msg), &data)
	if err != nil {
		return []string{}, err
	}

	snapshots := []string{}
	for _, v := range data {
		_, ok := v["name"]
		if !ok {
			return []string{}, errors.New("No \"name\" property found")
		}

		name, ok := v["name"].(string)
		if !ok {
			return []string{}, errors.New("\"name\" property did not have string type")
		}

		name = strings.TrimSpace(name)
		snapshots = append(snapshots, name)
	}

	if len(snapshots) == 0 {
		return []string{}, api.StatusErrorf(http.StatusNotFound, "Ceph RBD volume snapshot(s) not found")
	}

	return snapshots, nil
}

// getOSDPoolDefaultSize gets the global OSD default pool size that is used for
// all pools created without an explicit OSD pool size.
func (d *ceph) getOSDPoolDefaultSize() (int, error) {
	defaultSize, err := shared.TryRunCommand("ceph",
		"--name", "client."+d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"config",
		"get",
		"mon",
		"osd_pool_default_size",
		"--format",
		"json")
	if err != nil {
		return -1, err
	}

	var defaultSizeInt int
	err = json.Unmarshal([]byte(defaultSize), &defaultSizeInt)
	if err != nil {
		return -1, err
	}

	return defaultSizeInt, nil
}

// copyVolumeDiff creates a sparse copy of a volume by exporting and importing the diff
// between `sourceVolumeName` and its optional `sourceParentSnapshot` onto `targetVolumeName`.
// This does not introduce a dependency relation between the source RBD storage
// volume and the target RBD storage volume.
// Unlike the classic RBD export only the modified sectors on the RBD storage volume get copied.
func (d *ceph) copyVolumeDiff(sourceVolumeName string, targetVolumeName string, sourceParentSnapshot string) error {
	args := []string{
		"export-diff",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		sourceVolumeName,
	}

	if sourceParentSnapshot != "" {
		args = append(args, "--from-snap", sourceParentSnapshot)
	}

	// Redirect output to stdout.
	args = append(args, "-")

	rbdSendCmd := exec.Command("rbd", args...)
	rbdRecvCmd := exec.Command(
		"rbd",
		"import-diff",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"-",
		targetVolumeName)

	rbdRecvCmd.Stdin, _ = rbdSendCmd.StdoutPipe()
	rbdRecvCmd.Stdout = os.Stdout
	rbdRecvCmd.Stderr = os.Stderr

	d.logger.Debug("Copying RBD volume", logger.Ctx{"srcVolName": sourceVolumeName, "volName": targetVolumeName, "srcParentSnap": sourceParentSnapshot})

	err := rbdRecvCmd.Start()
	if err != nil {
		return err
	}

	err = rbdSendCmd.Run()
	if err != nil {
		return err
	}

	err = rbdRecvCmd.Wait()
	if err != nil {
		return err
	}

	return nil
}

// deleteVolume deletes the RBD storage volume of a container including any dependencies.
//   - This function takes care to delete any RBD storage entities that are marked
//     as zombie and whose existence is solely dependent on the RBD storage volume
//     for the container to be deleted.
//   - This function will mark any storage entities of the container to be deleted
//     as zombies in case any RBD storage entities in the storage pool have a
//     dependency relation with it.
//   - This function uses a C-style convention to return error or success simply
//     because it is more elegant and simple than the go way.
//     The function will return
//     -1 on error
//     0 if the RBD storage volume has been deleted
//     1 if the RBD storage volume has been marked as a zombie
//   - deleteVolume in conjunction with deleteVolumeSnapshot
//     recurses through an OSD storage pool to find and delete any storage
//     entities that were kept around because of dependency relations but are not
//     deletable.
func (d *ceph) deleteVolume(vol Volume) (int, error) {
	snaps, err := d.rbdListVolumeSnapshots(vol)
	if err == nil {
		var zombies int
		for _, snap := range snaps {
			ret, err := d.deleteVolumeSnapshot(vol, snap)
			if ret < 0 {
				return -1, err
			} else if ret == 1 {
				zombies++
			}
		}

		if zombies > 0 {
			// Unmap.
			err = d.rbdUnmapVolume(vol, true)
			if err != nil {
				return -1, err
			}

			if vol.isDeleted {
				return 1, nil
			}

			newVolumeName := fmt.Sprintf("%s_%s", vol.name, uuid.New().String())
			err := d.rbdMarkVolumeDeleted(vol, newVolumeName)
			if err != nil {
				return -1, err
			}

			return 1, nil
		} else if zombies == 0 {
			// Delete.
			err = d.rbdDeleteVolume(vol)
			if err != nil {
				return -1, err
			}
		}
	} else {
		if !response.IsNotFoundError(err) {
			return -1, err
		}

		parent, err := d.rbdGetVolumeParent(vol)
		if err == nil {
			parentVol, parentSnapshotName, err := d.parseParent(parent)
			if err != nil {
				return -1, err
			}

			// Unmap.
			err = d.rbdUnmapVolume(vol, true)
			if err != nil {
				return -1, err
			}

			// Delete.
			err = d.rbdDeleteVolume(vol)
			if err != nil {
				return -1, err
			}

			// Only delete the parent snapshot of the instance if it is a zombie.
			// This includes both if the parent volume itself is a zombie, or if the just the snapshot
			// is a zombie. If it is not we know that LXD is still using it.
			if parentVol.isDeleted || strings.HasPrefix(parentSnapshotName, "zombie_") {
				ret, err := d.deleteVolumeSnapshot(parentVol, parentSnapshotName)
				if ret < 0 {
					return -1, err
				}
			}
		} else {
			if !response.IsNotFoundError(err) {
				return -1, err
			}

			// Unmap.
			err = d.rbdUnmapVolume(vol, true)
			if err != nil {
				return -1, err
			}

			// Delete.
			err = d.rbdDeleteVolume(vol)
			if err != nil {
				return -1, err
			}
		}
	}

	return 0, nil
}

// deleteVolumeSnapshot deletes an RBD snapshot of a container including any dependencies.
//   - This function takes care to delete any RBD storage entities that are marked
//     as zombie and whose existence is solely dependent on the RBD snapshot for
//     the container to be deleted.
//   - This function will mark any storage entities of the container to be deleted
//     as zombies in case any RBD storage entities in the storage pool have a
//     dependency relation with it.
//   - This function uses a C-style convention to return error or success simply
//     because it is more elegant and simple than the go way.
//     The function will return
//     -1 on error
//     0 if the RBD storage volume has been deleted
//     1 if the RBD storage volume has been marked as a zombie
//   - deleteVolumeSnapshot in conjunction with deleteVolume
//     recurses through an OSD storage pool to find and delete any storage
//     entities that were kept around because of dependency relations but are not
//     deletable.
func (d *ceph) deleteVolumeSnapshot(vol Volume, snapshotName string) (int, error) {
	clones, err := d.rbdListSnapshotClones(vol, snapshotName)
	if err != nil {
		if !response.IsNotFoundError(err) {
			return -1, err
		}

		// Unprotect.
		err = d.rbdUnprotectVolumeSnapshot(vol, snapshotName)
		if err != nil {
			return -1, err
		}

		// Unmap.
		err = d.rbdUnmapVolumeSnapshot(vol, snapshotName, true)
		if err != nil {
			return -1, err
		}

		// Delete.
		err = d.rbdDeleteVolumeSnapshot(vol, snapshotName)
		if err != nil {
			return -1, err
		}

		// Only delete the parent image if it is a zombie. If it is not we know that LXD is still using it.
		if vol.isDeleted {
			ret, err := d.deleteVolume(vol)
			if ret < 0 {
				return -1, err
			}
		}

		return 0, nil
	}

	canDelete := true
	for _, clone := range clones {
		_, cloneType, cloneName, isDeleted, err := d.parseClone(clone)
		if err != nil {
			return -1, err
		}

		if !isDeleted {
			canDelete = false
			continue
		}

		cloneVol := NewVolume(d, d.name, VolumeType(cloneType), vol.contentType, cloneName, nil, nil)
		cloneVol.isDeleted = isDeleted

		ret, err := d.deleteVolume(cloneVol)
		if ret < 0 {
			return -1, err
		} else if ret == 1 {
			// Only marked as zombie.
			canDelete = false
		}
	}

	if canDelete {
		// Unprotect.
		err = d.rbdUnprotectVolumeSnapshot(vol, snapshotName)
		if err != nil {
			return -1, err
		}

		// Unmap.
		err = d.rbdUnmapVolumeSnapshot(vol, snapshotName, true)
		if err != nil {
			return -1, err
		}

		// Delete.
		err = d.rbdDeleteVolumeSnapshot(vol, snapshotName)
		if err != nil {
			return -1, err
		}

		// Only delete the parent image if it is a zombie. If it
		// is not we know that LXD is still using it.
		if vol.isDeleted {
			ret, err := d.deleteVolume(vol)
			if ret < 0 {
				return -1, err
			}
		}
	} else {
		if strings.HasPrefix(snapshotName, "zombie_") {
			return 1, nil
		}

		err := d.rbdUnmapVolumeSnapshot(vol, snapshotName, true)
		if err != nil {
			return -1, err
		}

		newSnapshotName := "zombie_snapshot_" + uuid.New().String()
		err = d.rbdRenameVolumeSnapshot(vol, snapshotName, newSnapshotName)
		if err != nil {
			return -1, err
		}
	}

	return 1, nil
}

// parseParent splits a string describing a RBD storage entity into its components.
// This can be used on strings like: <osd-pool-name>/<lxd-specific-prefix>_<rbd-storage-volume>@<rbd-snapshot-name>
// and will return a Volume and snapshot name.
func (d *ceph) parseParent(parent string) (Volume, string, error) {
	vol := Volume{}

	fields := strings.SplitN(parent, "/", 2)
	if len(fields) != 2 {
		return vol, "", errors.New("Pool delimiter not found")
	}

	parentName := fields[1]
	vol.pool = fields[0]
	vol.isDeleted = strings.HasPrefix(parentName, "zombie_")

	// Handle images.
	if strings.HasPrefix(parentName, "image_") || strings.HasPrefix(parentName, "zombie_image_") {
		vol.volType = VolumeTypeImage

		// Split snapshot name.
		s := strings.Split(parentName, "@")
		var name string
		var snapName string

		if len(s) > 1 {
			snapName = s[len(s)-1]
			name = strings.TrimSuffix(parentName, "@"+snapName)
		} else {
			name = parentName
		}

		// Remove prefix from name.
		name = strings.SplitN(name, "image_", 2)[1]

		// Check for block indicator.
		if strings.HasSuffix(name, ".block") {
			name = strings.TrimSuffix(name, ".block")
			vol.contentType = ContentTypeBlock
		} else {
			vol.contentType = ContentTypeFS
		}

		// Check for filesystem indicator.
		if strings.Contains(name, "_") {
			s := strings.Split(name, "_")
			filesystem := s[len(s)-1]

			name = strings.TrimSuffix(name, "_"+filesystem)
			vol.config = map[string]string{
				"block.filesystem": filesystem,
			}
		}

		vol.name = name
		return vol, snapName, nil
	}

	// Handle custom volumes.
	if strings.HasPrefix(parentName, "custom_") || strings.HasPrefix(parentName, "zombie_custom_") {
		vol.volType = VolumeTypeCustom

		// Split snapshot name.
		s := strings.Split(parentName, "@")
		var name string
		var snapName string

		if len(s) > 1 {
			snapName = s[len(s)-1]
			name = strings.TrimSuffix(parentName, "@"+snapName)
		} else {
			name = parentName
		}

		// Remove prefix from name.
		name = strings.SplitN(name, "custom_", 2)[1]

		// Check for block or ISO indicator.
		if strings.HasSuffix(name, ".block") {
			name = strings.TrimSuffix(name, ".block")
			vol.contentType = ContentTypeBlock
		} else if strings.HasSuffix(name, ".iso") {
			name = strings.TrimSuffix(name, ".iso")
			vol.contentType = ContentTypeISO
		} else {
			vol.contentType = ContentTypeFS
		}

		vol.name = name
		return vol, snapName, nil
	}

	// Handle container volumes.
	if strings.HasPrefix(parentName, "container_") || strings.HasPrefix(parentName, "zombie_container_") {
		vol.volType = VolumeTypeContainer

		// Split snapshot name.
		s := strings.Split(parentName, "@")
		var name string
		var snapName string

		if len(s) > 1 {
			snapName = s[len(s)-1]
			name = strings.TrimSuffix(parentName, "@"+snapName)
		} else {
			name = parentName
		}

		// Remove prefix from name.
		name = strings.SplitN(name, "container_", 2)[1]

		// Check for block indicator.
		if strings.HasSuffix(name, ".block") {
			name = strings.TrimSuffix(name, ".block")
			vol.contentType = ContentTypeBlock
		} else {
			vol.contentType = ContentTypeFS
		}

		vol.name = name
		return vol, snapName, nil
	}

	// Handle virtual-machines volumes.
	if strings.HasPrefix(parentName, "virtual_machine_") || strings.HasPrefix(parentName, "zombie_virtual_machine_") {
		vol.volType = VolumeTypeVM

		// Split snapshot name.
		s := strings.Split(parentName, "@")
		var name string
		var snapName string

		if len(s) > 1 {
			snapName = s[len(s)-1]
			name = strings.TrimSuffix(parentName, "@"+snapName)
		} else {
			name = parentName
		}

		// Remove prefix from name.
		name = strings.SplitN(name, "virtual_machine_", 2)[1]

		// Check for block indicator.
		if strings.HasSuffix(name, ".block") {
			name = strings.TrimSuffix(name, ".block")
			vol.contentType = ContentTypeBlock
		} else {
			vol.contentType = ContentTypeFS
		}

		vol.name = name
		return vol, snapName, nil
	}

	return vol, "", fmt.Errorf("Unrecognized parent: %q", parent)
}

// parseClone splits a strings describing an RBD storage volume.
// For example a string like
// <osd-pool-name>/<lxd-specific-prefix>_<rbd-storage-volume>
// will be split into
// <osd-pool-name>, <prefix>, <rbd-storage-volume>.
func (d *ceph) parseClone(clone string) (poolName string, volumeType string, volumeName string, volumeDeleted bool, err error) {
	poolName, volumeName, found := strings.Cut(clone, "/")
	if !found {
		return "", "", "", false, errors.New("Pool delimiter not found")
	}

	// Strip zombie prefix.
	volumeName, volumeDeleted = strings.CutPrefix(volumeName, "zombie_")

	volumeType, volumeName, found = strings.Cut(volumeName, "_")
	if !found {
		return "", "", "", false, errors.New("Unexpected parsing error")
	}

	return poolName, volumeType, volumeName, volumeDeleted, nil
}

// getRBDMappedDevPath looks at sysfs to retrieve the device path. If it doesn't find it it will map it if told to
// do so. Returns bool indicating if map was needed and device path e.g. "/dev/rbd<idx>" for an RBD image.
func (d *ceph) getRBDMappedDevPath(vol Volume, mapIfMissing bool) (bool, string, error) {
	// List all RBD devices.
	files, err := os.ReadDir("/sys/devices/rbd")
	if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}

	// Go through the existing RBD devices.
	for _, f := range files {
		fName := f.Name()

		// Skip if not a directory.
		if !f.IsDir() {
			continue
		}

		// Skip if not a device directory.
		idx, err := strconv.ParseUint(fName, 10, 64)
		if err != nil {
			continue
		}

		// Get the pool for the RBD device.
		devPoolName, err := os.ReadFile(fmt.Sprintf("/sys/devices/rbd/%s/pool", fName))
		if err != nil {
			// Skip if no pool file.
			if os.IsNotExist(err) {
				continue
			}

			return false, "", err
		}

		// Skip if the pools don't match.
		if strings.TrimSpace(string(devPoolName)) != d.config["ceph.osd.pool_name"] {
			continue
		}

		// Get the volume name for the RBD device.
		devName, err := os.ReadFile(fmt.Sprintf("/sys/devices/rbd/%s/name", fName))
		if err != nil {
			// Skip if no name file.
			if os.IsNotExist(err) {
				continue
			}

			return false, "", err
		}

		rbdName := d.getRBDVolumeName(vol, "", false)

		// Split RBD name into volume name and snapshot name parts.
		rbdNameParts := strings.SplitN(rbdName, "@", 2)

		// Skip if the names don't match (excluding snapshot part of RBD volume name).
		if strings.TrimSpace(string(devName)) != rbdNameParts[0] {
			continue
		}

		// Get the snapshot name for the RBD device (if exists).
		devSnap, err := os.ReadFile(fmt.Sprintf("/sys/devices/rbd/%s/current_snap", fName))
		if err != nil && !os.IsNotExist(err) {
			return false, "", err
		}

		devSnapName := strings.TrimSpace(string(devSnap))

		if vol.IsSnapshot() {
			// Volume is a snapshot, check device's snapshot name matches the volume's snapshot name.
			if len(rbdNameParts) == 2 && rbdNameParts[1] == devSnapName {
				return false, fmt.Sprintf("/dev/rbd%d", idx), nil // We found a match.
			}
		} else if slices.Contains([]string{"-", ""}, devSnapName) {
			// Volume is not a snapshot and neither is this device.
			return false, fmt.Sprintf("/dev/rbd%d", idx), nil // We found a match.
		}

		continue
	}

	// No device could be found, map it ourselves.
	if mapIfMissing {
		devPath, err := d.rbdMapVolume(vol)
		if err != nil {
			return false, "", err
		}

		return true, devPath, nil
	}

	return false, "", fmt.Errorf("Volume %q not mapped to an RBD device", vol.Name())
}

// generateUUID regenerates the XFS/btrfs UUID as needed.
func (d *ceph) generateUUID(fsType string, devPath string) error {
	if !renegerateFilesystemUUIDNeeded(fsType) {
		return nil
	}

	// Update the UUID.
	d.logger.Debug("Regenerating filesystem UUID", logger.Ctx{"dev": devPath, "fs": fsType})
	err := regenerateFilesystemUUID(fsType, devPath)
	if err != nil {
		return err
	}

	return nil
}

func (d *ceph) getRBDVolumeName(vol Volume, snapName string, withPoolName bool) string {
	imageName, volSnapName := CephGetRBDImageName(vol, vol.isDeleted)

	// see `man 8 rbd` for "snap-spec"
	if snapName != "" {
		imageName += "@" + snapName
	} else if volSnapName != "" {
		imageName += "@" + volSnapName
	}

	// If needed, the output will be prefixed with the pool name, e.g.
	// <pool>/<type>_<volname>@<snapname>.
	if withPoolName {
		imageName = d.config["ceph.osd.pool_name"] + "/" + imageName
	}

	return imageName
}

// Let's say we want to send the a container "a" including snapshots "snap0" and
// "snap1" on storage pool "pool1" from LXD "l1" to LXD "l2" on storage pool
// "pool2":
//
// The pool layout on "l1" would be:
//
//	pool1/container_a
//	pool1/container_a@snapshot_snap0
//	pool1/container_a@snapshot_snap1
//
// Then we need to send:
//
//	rbd export-diff pool1/container_a@snapshot_snap0 - | rbd import-diff - pool2/container_a
//
// (Note that pool2/container_a must have been created by the receiving LXD
// instance before.)
//
//	rbd export-diff pool1/container_a@snapshot_snap1 --from-snap snapshot_snap0 - | rbd import-diff - pool2/container_a
//	rbd export-diff pool1/container_a --from-snap snapshot_snap1 - | rbd import-diff - pool2/container_a
func (d *ceph) sendVolume(conn io.ReadWriteCloser, volumeName string, volumeParentName string, tracker *ioprogress.ProgressTracker) error {
	defer func() { _ = conn.Close() }()

	args := []string{
		"export-diff",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		volumeName,
	}

	if volumeParentName != "" {
		args = append(args, "--from-snap", volumeParentName)
	}

	// Redirect output to stdout.
	args = append(args, "-")

	cmd := exec.Command("rbd", args...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	// Setup progress tracker.
	var stdout io.WriteCloser = conn
	if tracker != nil {
		stdout = &ioprogress.ProgressWriter{
			WriteCloser: conn,
			Tracker:     tracker,
		}
	}

	cmd.Stdout = stdout

	d.logger.Debug("Sending RBD volume", logger.Ctx{"volName": volumeName, "volParentName": volumeParentName})

	err = cmd.Start()
	if err != nil {
		return err
	}

	output, _ := io.ReadAll(stderr)

	// Handle errors.
	err = cmd.Wait()
	if err != nil {
		return fmt.Errorf("ceph export-diff failed: %w (%s)", err, string(output))
	}

	return nil
}

func (d *ceph) receiveVolume(volumeName string, conn io.ReadWriteCloser, writeWrapper func(io.WriteCloser) io.WriteCloser) error {
	args := []string{
		"import-diff",
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"-",
		volumeName,
	}

	cmd := exec.Command("rbd", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	// Forward input through stdin.
	chCopyConn := make(chan error, 1)
	go func() {
		_, err = io.Copy(stdin, conn)
		_ = stdin.Close()
		chCopyConn <- err
	}()

	d.logger.Debug("Receiving RBD volume", logger.Ctx{"volName": volumeName})

	// Run the command.
	err = cmd.Start()
	if err != nil {
		return err
	}

	// Read any error.
	output, _ := io.ReadAll(stderr)

	// Handle errors.
	errs := []error{}
	chCopyConnErr := <-chCopyConn

	err = cmd.Wait()
	if err != nil {
		errs = append(errs, err)

		if chCopyConnErr != nil {
			errs = append(errs, chCopyConnErr)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("Problem with ceph import-diff: (%v) %s", errs, string(output))
	}

	return nil
}

// resizeVolume resizes an RBD volume. This function does not resize any filesystem inside the RBD volume.
func (d *ceph) resizeVolume(vol Volume, sizeBytes int64, allowShrink bool) error {
	args := []string{
		"resize",
	}

	if allowShrink {
		args = append(args, "--allow-shrink")
	}

	args = append(args,
		"--id", d.config["ceph.user.name"],
		"--cluster", d.config["ceph.cluster_name"],
		"--pool", d.config["ceph.osd.pool_name"],
		"--size", fmt.Sprintf("%dB", sizeBytes),
		d.getRBDVolumeName(vol, "", false),
	)

	// Resize the block device.
	_, err := shared.TryRunCommand("rbd", args...)

	return err
}

// findLastCommonSnapshotIndex finds the last common snapshot from the list of targetSnapshots based on its name.
// The list of targetSnapshots represents the wanted list of snapshots on the target volume.
// This list is identical to the target volume's snapshots in the database.
// In refreshSnapshots provide a list of snapshot names that require refresh on the target side.
// The returned number is the index of the last common snapshot in the list of targetSnapshots.
//
// The function identifies the last common snapshot based on the following criteria:
//  1. (Ideal case)
//     There aren't any snapshots marked for refresh.
//     Return the index for the last snapshot as this is the last common one.
//  2. If the first target snapshot matches the first one that requires refresh,
//     there isn't any common snapshot. Return -1.
//  3. If the last target snapshot is not the last one that requires refresh,
//     the snapshots are out of sync.
//     The last common snapshot is the predecessor of the first one that requires refresh.
//     Return the index of the last common snapshot.
//  4. The target is missing the last x snapshots from the source.
//     The last common snapshot is the predecessor of the first one that requires refresh.
//     Return the index of the last common snapshot.
//  5. If there isn't a single target snapshot return -1.
func (d *ceph) findLastCommonSnapshotIndex(targetSnapshots []Volume, refreshSnapshots []string) int {
	if len(targetSnapshots) > 0 {
		// Case 1:
		// The volume snapshots on the source and target might look like this:
		// sourceVol   ->   targetVol
		// \_ snap0         \_ snap0
		// \_ snap1         \_ snap1
		// \_ snap2         \_ snap2
		if len(refreshSnapshots) == 0 {
			return len(targetSnapshots) - 1
		}

		// Case 2:
		// The volume snapshots on the source and target might look like this:
		// sourceVol   ->   targetVol
		// \_ snap0
		// \_ snap1
		// \_ snap2
		_, firstTargetSnapshotName, _ := api.GetParentAndSnapshotName(targetSnapshots[0].name)
		if firstTargetSnapshotName == refreshSnapshots[0] {
			return -1
		}

		// Case 3:
		// The volume snapshots on the source and target might look like this:
		// sourceVol   ->   targetVol
		// \_ snap0         \_ snap0
		// \_ snap1         \_ snap2
		// \_ snap2
		// Case 4:
		// The volume snapshots on the source and target might look like this:
		// sourceVol   ->   targetVol
		// \_ snap0         \_ snap0
		// \_ snap1         \_ snap1
		// \_ snap2
		for i, targetSnapshot := range targetSnapshots {
			// Find the last common snapshot between the source and target.
			// Start by looking up the position of the first snapshot that requires a refresh.
			_, targetSnapshotName, _ := api.GetParentAndSnapshotName(targetSnapshot.name)
			if targetSnapshotName == refreshSnapshots[0] {
				return i - 1
			}
		}
	}

	// Case 5.
	// The volume snapshots on the source and target might look like this:
	// sourceVol   ->   targetVol
	return -1
}
