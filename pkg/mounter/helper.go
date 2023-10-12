package mounter

import (
	"strings"

	mountutils "k8s.io/mount-utils"
)

// Copy from https://github.com/kubernetes/mount-utils/blob/41e8de37ef8a3782f9cd6c915699b98b2b24b2c4/mount_helper_unix.go#L164
func SplitMountOptions(s string) []string {
	inQuotes := false
	list := strings.FieldsFunc(s, func(r rune) bool {
		if r == '"' {
			inQuotes = !inQuotes
		}
		// Report a new field only when outside of double quotes.
		return r == ',' && !inQuotes
	})
	return list
}

func GetMountpointType(mounter mountutils.Interface, path string) (string, bool, error) {
	mountpoints, err := mounter.List()
	if err != nil {
		return "", false, err
	}
	for _, mp := range mountpoints {
		if mp.Path == path {
			return mp.Type, true, nil
		}
	}
	return "", false, nil
}
