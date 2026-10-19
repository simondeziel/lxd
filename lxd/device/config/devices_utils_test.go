package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/canonical/lxd/shared/validate"
)

func TestFileName(t *testing.T) {
	devNames := []string{"proxy", "proxy-1", "proxy/1", "proxy--1", "proxy//1", "a..b", "x/../../escaped", "../../../../etc/cron.d/x"}
	fileNames := map[string]bool{}
	for _, devName := range devNames {
		fileName := FileName(devName)
		assert.False(t, fileNames[fileName], "Duplicate file name %q for device %q", fileName, devName)
		fileNames[fileName] = true

		assert.NotContains(t, fileName, "/", "File name %q for device %q contains a slash", fileName, devName)

		if strings.Contains(devName, "/") {
			assert.Error(t, validate.IsDeviceName(fileName), "File name %q for device %q is a valid device name", fileName, devName)
		} else {
			assert.Equal(t, devName, fileName, "File name for device %q changed", devName)
		}
	}
}

func TestDeviceEqualsDiffKeys(t *testing.T) {
	tests := []struct {
		name         string
		old          Device
		new          Device
		expectedKeys []string
	}{
		{
			name:         "IdenticalDevices",
			old:          Device{"type": "nic", "nictype": "bridged"},
			new:          Device{"type": "nic", "nictype": "bridged"},
			expectedKeys: nil,
		},
		{
			name:         "BothEmpty",
			old:          Device{},
			new:          Device{},
			expectedKeys: nil,
		},
		{
			name:         "ChangedValue",
			old:          Device{"type": "nic", "nictype": "bridged"},
			new:          Device{"type": "nic", "nictype": "macvlan"},
			expectedKeys: []string{"nictype"},
		},
		{
			name:         "AddedKey",
			old:          Device{"type": "nic"},
			new:          Device{"type": "nic", "nictype": "bridged"},
			expectedKeys: []string{"nictype"},
		},
		{
			name:         "RemovedKey",
			old:          Device{"type": "nic", "nictype": "bridged"},
			new:          Device{"type": "nic"},
			expectedKeys: []string{"nictype"},
		},
		{
			name:         "MultipleChanges",
			old:          Device{"type": "nic", "nictype": "bridged", "parent": "eth0"},
			new:          Device{"type": "nic", "nictype": "macvlan", "mtu": "1500"},
			expectedKeys: []string{"nictype", "mtu", "parent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deviceEqualsDiffKeys(tt.old, tt.new)
			assert.ElementsMatch(t, tt.expectedKeys, result)
		})
	}
}
