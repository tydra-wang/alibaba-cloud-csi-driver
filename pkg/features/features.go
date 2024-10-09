package features

import (
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/component-base/featuregate"
)

const (
	// Do disk attach/detach at controller, not node.
	// Historically, disks don't have serial number, so we need to attach/detach disks at node
	// to identify which device is the just attached disk.
	// If all your disks are created after 2020-06-10, you can enable this safely.
	// See: https://www.alibabacloud.com/help/en/ecs/user-guide/query-the-serial-number-of-a-disk
	//
	// Enable this at controller first, wait for the rollout to finish, then enable this at node.
	DiskADController featuregate.Feature = "DiskADController"

	// Attach multiple disks to the same node in parallel.
	// ECS don't allow parallel attach/detach to a node by default.
	// Enable this if you need faster attach, and only if your UID is whitelisted (by open a ticket),
	// or you have the supportConcurrencyAttach=true tag on your ECS instance.
	//
	// Only effective when DiskADController is also enabled.
	DiskParallelAttach featuregate.Feature = "DiskParallelAttach"

	// Detach multiple disks from the same node in parallel.
	// ECS does not allow parallel detach from a node currently. This feature gate is reserved for future use.
	//
	// Only effective when DiskADController is also enabled.
	DiskParallelDetach featuregate.Feature = "DiskParallelDetach"

	AlinasMountProxy featuregate.Feature = "AlinasMountProxy"

	UpdatedOssfsVersion featuregate.Feature = "UpdatedOssfsVersion"

	RundCSIProtocol3 featuregate.Feature = "RundCSIProtocol3"
)

var (
	FunctionalMutableFeatureGate = featuregate.NewFeatureGate()
	diskFeatures                 = map[featuregate.Feature]featuregate.FeatureSpec{
		DiskADController:   {Default: false, PreRelease: featuregate.Alpha},
		DiskParallelAttach: {Default: false, PreRelease: featuregate.Alpha},
		DiskParallelDetach: {Default: false, PreRelease: featuregate.Alpha},
	}
	ossFeatures = map[featuregate.Feature]featuregate.FeatureSpec{
		UpdatedOssfsVersion: {Default: true, PreRelease: featuregate.Beta},
	}
	nasFeatures = map[featuregate.Feature]featuregate.FeatureSpec{
		AlinasMountProxy: {Default: false, PreRelease: featuregate.Alpha},
	}
	otherFeatures = map[featuregate.Feature]featuregate.FeatureSpec{
		RundCSIProtocol3: {Default: false, PreRelease: featuregate.Alpha},
	}
)

func init() {
	runtime.Must(FunctionalMutableFeatureGate.Add(diskFeatures))
	runtime.Must(FunctionalMutableFeatureGate.Add(ossFeatures))
	runtime.Must(FunctionalMutableFeatureGate.Add(nasFeatures))
	runtime.Must(FunctionalMutableFeatureGate.Add(otherFeatures))
}
