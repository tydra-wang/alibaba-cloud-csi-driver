package mounter

import (
	"context"
	"fmt"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"google.golang.org/grpc"
	mountutils "k8s.io/mount-utils"
)

const (
	proxyDefaultTimeout = time.Minute
)

type ProxyMounter struct {
	mountutils.Interface

	client  proxy.MountProxyClient
	timeout time.Duration
}

func (m *ProxyMounter) Mount(source string, target string, fstype string, options []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	_, err := m.client.Mount(ctx, &proxy.MountRequest{
		Source:  source,
		Target:  target,
		Fstype:  fstype,
		Options: options,
	})
	if err != nil {
		return fmt.Errorf("mount proxy: %v", err)
	}
	return nil
}

func NewProxyMounter(proxyEndpoint string) (mountutils.Interface, error) {
	cc, err := grpc.Dial(proxyEndpoint)
	if err != nil {
		return nil, err
	}
	client := proxy.NewMountProxyClient(cc)
	return &ProxyMounter{
		Interface: mountutils.New(""),
		client:    client,
		timeout:   proxyDefaultTimeout,
	}, nil
}
