package internal

import (
	cnfsv1beta1 "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cnfs/v1beta1"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/nas/cloud"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
)

type ControllerConfig struct {
	// cluster info
	Region    string
	ClusterID string

	// subpath configs
	SkipSubpathCreation    bool
	EnableSubpathFinalizer bool

	// clients for kubernetes
	KubeClient    kubernetes.Interface
	CNFSGetter    cnfsv1beta1.CNFSGetter
	EventRecorder record.EventRecorder

	// clients for alibaba cloud
	NasClientFactory *cloud.NasClientFactory
}

func GetControllerConfig(client kubernetes.Interface) (*ControllerConfig, error) {
	// TODO
	return nil, nil
}
