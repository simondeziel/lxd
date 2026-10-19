package device

import (
	"path/filepath"
	"testing"

	"github.com/canonical/lxd/lxd/device/cdi"
)

func TestCDIFilePaths(t *testing.T) {
	devicesPath := "/var/lib/lxd/devices/c1"

	for _, devName := range []string{"gpu", "gpu-0", "gpu--0", "a..b", "x/../../escaped", "x/../../../../../etc/x"} {
		for _, path := range []string{cdiHooksFilePath(devicesPath, devName), cdiConfigDevicesFilePath(devicesPath, devName)} {
			if filepath.Dir(path) != devicesPath {
				t.Errorf("Path %q for device %q is outside of %q", path, devName, devicesPath)
			}
		}
	}

	// Names without "/" keep the paths that earlier LXD versions used.
	for _, devName := range []string{"gpu-0", "gpu--0"} {
		want := filepath.Join(devicesPath, devName+cdi.CDIHooksFileSuffix)
		got := cdiHooksFilePath(devicesPath, devName)
		if got != want {
			t.Errorf("Expected %q for device %q, got %q", want, devName, got)
		}
	}
}
