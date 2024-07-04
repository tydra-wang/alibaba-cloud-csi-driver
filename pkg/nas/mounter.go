package nas

import (
	"fmt"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/sirupsen/logrus"
	mountutils "k8s.io/mount-utils"
)

const (
	alinasUtilsProxySocket = "/var/run/cnfs/alinas/alinas.sock"
)

type NasMounter struct {
	mountutils.Interface
	connectorMounter mountutils.Interface
}

func (m *NasMounter) Mount(source string, target string, fstype string, options []string) error {
	mt := m.Interface
	switch fstype {
	case "alinas", "cpfs-nfs":
		if utils.IsFileExisting(alinasUtilsProxySocket) {
			logrus.Infof("using alinas mount proxy because %s detected", alinasUtilsProxySocket)
			var err error
			mt, err = mounter.NewProxyMounterUnix(alinasUtilsProxySocket)
			if err != nil {
				return fmt.Errorf("init proxy mounter: %w", err)
			}
		} else {
			mt = m.connectorMounter
		}
	case "cpfs":
		mt = m.connectorMounter
	}
	log := logrus.WithFields(logrus.Fields{
		"source":       source,
		"target":       target,
		"mountOptions": options,
		"fstype":       fstype,
	})
	err := mt.Mount(source, target, fstype, options)
	if err != nil {
		log.Errorf("failed to mount: %v", err)
	} else {
		log.Info("mounted successfully")
	}
	return err
}

func NewNasMounter() mountutils.Interface {
	inner := mountutils.New("")
	return &NasMounter{
		Interface:        inner,
		connectorMounter: mounter.NewConnectorMounter(inner, ""),
	}
}
