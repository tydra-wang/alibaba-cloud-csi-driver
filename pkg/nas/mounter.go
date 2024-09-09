package nas

import (
	"os"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"k8s.io/klog/v2"
	mountutils "k8s.io/mount-utils"
)

const defaultAlinasSocket = "/run/cnfs/alinas-mounter.sock"

type NasMounter struct {
	mountutils.Interface
	connectorMounter mountutils.Interface
	proxyMounter     mountutils.Interface
}

func (m *NasMounter) Mount(source string, target string, fstype string, options []string) (err error) {
	logger := klog.Background().WithValues(
		"source", source,
		"target", target,
		"options", options,
		"fstype", fstype,
	)
	switch fstype {
	case "cpfs":
		err = m.connectorMounter.Mount(source, target, fstype, options)
	case "alinas", "cpfs-nfs":
		if features.FunctionalMutableFeatureGate.Enabled(features.AlinasMountProxy) {
			err = m.proxyMounter.Mount(source, target, fstype, options)
		} else {
			err = m.connectorMounter.Mount(source, target, fstype, options)
		}
	default:
		err = m.Interface.Mount(source, target, fstype, options)
	}
	if err != nil {
		logger.Error(err, "failed to mount")
	} else {
		logger.Info("mounted successfully")
	}
	return err
}

func NewNasMounter() mountutils.Interface {
	inner := mountutils.New("")
	socket := os.Getenv("ALINAS_SOCKET")
	if socket == "" {
		socket = defaultAlinasSocket
	}
	return &NasMounter{
		Interface:        inner,
		connectorMounter: mounter.NewConnectorMounter(inner, ""),
		proxyMounter:     mounter.NewProxyMounter(socket, inner),
	}
}
