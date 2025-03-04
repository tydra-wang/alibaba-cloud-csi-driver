package server

import (
	"context"
	"fmt"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
)

type MountHandler interface {
	Name() string
	Fstypes() []string
	Mount(ctx context.Context, req *proxy.MountRequest) error
	Init()
	Terminate()
}

var (
	fstypeToHandler = map[string]MountHandler{}
	nameToHandler   = map[string]MountHandler{}
)

func RegisterMountHandler(handler MountHandler) {
	nameToHandler[handler.Name()] = handler
}

func handleMountRequest(ctx context.Context, req *proxy.MountRequest) error {
	h := fstypeToHandler[req.Fstype]
	if h == nil {
		return fmt.Errorf("fstype %q not supported", req.Fstype)
	}
	return h.Mount(ctx, req)
}
