/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http:// www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dfs

import (
	"context"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

type nodeServer struct {
	common.GenericNodeServer
	mounter mount.Interface
}

func newNodeServer(nodeId string) *nodeServer {
	return &nodeServer{
		mounter: mount.New(""),
		GenericNodeServer: common.GenericNodeServer{
			NodeID: nodeId,
		},
	}
}

func (n *nodeServer) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	klog.Info("DFS NodePublishVolume")
	notMnt, err := n.mounter.IsLikelyNotMountPoint(req.TargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(req.TargetPath, os.ModePerm); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			notMnt = true
		} else {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if !notMnt {
		klog.Infof("NodePublishVolume: %s already mounted", req.TargetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	err = n.mounter.Mount("tmpfs", req.TargetPath, "tmpfs", []string{"ro"})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Mount tmpfs: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (n *nodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	klog.Info("DFS NodeUnpublishVolume")
	err := mount.CleanupMountPoint(req.TargetPath, n.mounter, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Cleanup mount point: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (n *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	klog.Info("DFS NodeStageVolume")
	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	klog.Info("DFS NodeUnstageVolume")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (n *nodeServer) NodeGetCapabilities(context context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
				},
			},
		},
	}}, nil
}
