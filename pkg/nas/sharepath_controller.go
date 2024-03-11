/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http:// www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nas

import (
	"context"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/nas/internal"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
)

type sharepathController struct{}

func newSharepathController(_ *internal.ControllerConfig) (internal.Controller, error) {
	return &sharepathController{}, nil
}

func (cs *sharepathController) VolumeAs() string {
	return "sharepath"
}

func (cs *sharepathController) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	parameters := req.Parameters
	reclaimPolicy, ok := parameters[csiAlibabaCloudName+"/"+"reclaimPolicy"]
	if ok && reclaimPolicy != string(corev1.PersistentVolumeReclaimRetain) {
		return nil, status.Errorf(codes.InvalidArgument, "Use sharepath mode, reclaimPolicy must be Retain. The current reclaimPolicy is %q", reclaimPolicy)
	}
	volumeContext := map[string]string{}
	// using cnfs or not
	if cnfsName := parameters["containerNetworkFileSystem"]; cnfsName != "" {
		path := parameters["path"]
		if path == "" {
			path = "/"
		} else {
			path = filepath.Clean(path)
		}
		volumeContext["containerNetworkFileSystem"] = cnfsName
		volumeContext["path"] = path
	} else {
		server, path := muxServerSelector.SelectNfsServer(parameters["server"])
		if server == "" {
			return nil, status.Error(codes.InvalidArgument, "invalid nas server")
		}
		if protocol := parameters["mountProtocol"]; protocol != "" {
			volumeContext["mountProtocol"] = protocol
		}
		volumeContext["server"] = server
		volumeContext["path"] = path
	}

	// fill volumeContext
	if mountType := parameters["mountType"]; mountType != "" {
		volumeContext["mountType"] = mountType
	}

	capacity := req.GetCapacityRange().GetRequiredBytes()
	resp := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      req.Name,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
	return resp, nil
}

func (cs *sharepathController) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest, pv *corev1.PersistentVolume) (*csi.DeleteVolumeResponse, error) {
	log.Warn("skip deleting volume as sharepath")
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *sharepathController) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest, pv *corev1.PersistentVolume) (*csi.ControllerExpandVolumeResponse, error) {
	log.Warn("skip expansion for volume as sharepath")
	return &csi.ControllerExpandVolumeResponse{CapacityBytes: req.CapacityRange.RequiredBytes}, nil
}
