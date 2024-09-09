package mounter

import (
	"errors"
	"fmt"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/client"
	mountutils "k8s.io/mount-utils"
)

type ProxyMounter struct {
	client client.Client
	mountutils.Interface
}

func NewProxyMounter(socketPath string, inner mountutils.Interface) *ProxyMounter {
	return &ProxyMounter{
		client:    client.NewClient(socketPath),
		Interface: inner,
	}
}

func (m *ProxyMounter) MountWithSecrets(source, target, fstype string, options []string, secrets map[string]string) error {
	resp, err := m.client.Mount(&proxy.MountRequest{
		Source:  source,
		Target:  target,
		Fstype:  fstype,
		Options: options,
		Secrets: secrets,
	})
	if err != nil {
		return fmt.Errorf("call mounter daemon: %w", err)
	}
	err = resp.ToError()
	if err != nil {
		return fmt.Errorf("failed to mount: %w", err)
	}
	notMnt, err := mountutils.IsNotMountPoint(m.Interface, target)
	if err != nil {
		return err
	}
	if notMnt {
		return errors.New("failed to mount")
	}
	return nil
}

func (m *ProxyMounter) Mount(source string, target string, fstype string, options []string) error {
	return m.MountWithSecrets(source, target, fstype, options, nil)
}
