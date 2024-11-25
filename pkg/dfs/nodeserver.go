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
	klog.InfoS("DFS NodePublishVolume", "targetPath", req.TargetPath, "volumeId", req.VolumeId)
	if req.GetVolumeCapability().GetBlock() == nil {
		return nil, status.Error(codes.InvalidArgument, "Only block mode supported")
	}
	notMnt, err := n.mounter.IsLikelyNotMountPoint(req.TargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			f, err := os.OpenFile(req.TargetPath, os.O_CREATE, 0644)
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			defer f.Close()
			notMnt = true
		} else {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if !notMnt {
		klog.InfoS("NodePublishVolume: already mounted", "targetPath", req.TargetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	mountOptions := []string{"bind"}
	if req.Readonly {
		mountOptions = append(mountOptions, "ro")
	}

	devicePath := req.VolumeContext["devicePath"]
	if devicePath == "" {
		devicePath = req.PublishContext["devicePath"]
	}

	if devicePath == "" {
		return nil, status.Error(codes.Internal, "devicePath not found in publishContext")
	}

	err = n.mounter.Mount(devicePath, req.TargetPath, "", mountOptions)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Mount tmpfs: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (n *nodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	klog.Info("DFS NodeUnpublishVolume", "targetPath", req.TargetPath)
	err := mount.CleanupMountPoint(req.TargetPath, n.mounter, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Cleanup mount point: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}
