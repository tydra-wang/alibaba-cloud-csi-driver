package mounter

import mountutils "k8s.io/mount-utils"

var _ mountutils.Interface = &ConnectorMounter{}

type ConnectorMounter struct {
	mountutils.Interface
}

func NewConnectorMounter() mountutils.Interface {
	return &ConnectorMounter{
		Interface: mountutils.New(""),
	}
}
