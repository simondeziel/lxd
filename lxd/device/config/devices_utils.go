package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// FileName returns an identifier for the named device that is safe to use as, or as part of, a file name.
func FileName(devName string) string {
	// Names without "/" are a single path component, and keeping them lets LXD find what earlier versions created.
	if !strings.Contains(devName, "/") {
		return devName
	}

	// Device names are at most 63 characters, so this can't be another device's name.
	sum := sha256.Sum256([]byte(devName))
	return hex.EncodeToString(sum[:])
}

// deviceEqualsDiffKeys checks for any difference and addition/removal of properties and returns a list of changes.
func deviceEqualsDiffKeys(old Device, d Device) []string {
	var keys []string

	for k := range d {
		if d[k] != old[k] {
			keys = append(keys, k)
		}
	}

	for k := range old {
		_, found := d[k]
		if !found {
			keys = append(keys, k)
		}
	}

	return keys
}
