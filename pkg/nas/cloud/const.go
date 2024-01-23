package cloud

import (
	"fmt"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/version"
)

const (
	FilesystemTypeStandard = "standard"
)

var KubernetesAlicloudIdentity = fmt.Sprintf("Kubernetes.Alicloud/CsiProvision.Nas-%s", version.VERSION)
