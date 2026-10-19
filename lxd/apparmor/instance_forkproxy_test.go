package apparmor

import (
	"path/filepath"
	"testing"

	"github.com/canonical/lxd/shared/api"
)

type forkproxyTestInstance struct {
	instance
}

func (forkproxyTestInstance) Project() api.Project {
	return api.Project{Name: "default"}
}

func (forkproxyTestInstance) Name() string {
	return "c1"
}

type forkproxyTestDevice struct {
	device

	name string
}

func (d forkproxyTestDevice) Name() string {
	return d.name
}

func TestForkproxyProfileFilename(t *testing.T) {
	inst := forkproxyTestInstance{}
	for _, devName := range []string{"proxy", "proxy-1", "proxy/1", "a..b", "x/../../escaped", "../../../../etc/cron.d/x"} {
		filename := forkproxyProfileFilename(inst, forkproxyTestDevice{name: devName})
		path := filepath.Join(aaPath, "profiles", filename)
		if filepath.Dir(path) != filepath.Join(aaPath, "profiles") {
			t.Errorf("Profile path %q for device %q is outside of the profiles directory", path, devName)
		}
	}
}
