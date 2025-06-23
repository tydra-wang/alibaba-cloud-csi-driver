package alinas

import (
	"testing"

	"k8s.io/utils/strings/slices"
)

func Test_addAutoFallbackNFSMountOptions(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		mountOptions []string
		want         []string
	}{
		{
			"do not add auto_fallback_nfs if not using efc",
			[]string{"vers=3"},
			[]string{"vers=3"},
		},
		{
			"add auto_fallback_nfs option for efc",
			[]string{"efc,vers=3"},
			[]string{"efc,vers=3", "auto_fallback_nfs"},
		},
		{
			"do not add auto_fallback_nfs if using vsc",
			[]string{"efc,protocol=efc,fstype=cpfs", "_netdev,net=vsc"},
			[]string{"efc,protocol=efc,fstype=cpfs", "_netdev,net=vsc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addAutoFallbackNFSMountOptions(tt.mountOptions)
			if !slices.Equal(got, tt.want) {
				t.Errorf("addAutoFallbackNFSMountOptions() = %v, want %v", got, tt.want)
			}
		})
	}
}
