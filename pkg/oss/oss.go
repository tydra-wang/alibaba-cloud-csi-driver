/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package oss

import (
	"context"
	"fmt"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	csicommon "github.com/kubernetes-csi/drivers/pkg/csi-common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	cnfsv1beta1 "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cnfs/v1beta1"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/options"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/version"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	mountutils "k8s.io/mount-utils"
)

const (
	driverName = "ossplugin.csi.alibabacloud.com"
)

// OSS the OSS object
type OSS struct {
	driver   *csicommon.CSIDriver
	endpoint string

	controllerServer csi.ControllerServer
	nodeServer       csi.NodeServer
}

// NewDriver init oss type of csi driver
func NewDriver(endpoint string, m metadata.MetadataProvider, runAsController bool) *OSS {
	log.Infof("Driver: %v version: %v", driverName, version.VERSION)
	nodeName := os.Getenv("KUBE_NODE_NAME")
	if nodeName == "" {
		log.Fatal("env KUBE_NODE_NAME is empty")
	}
	instanceId := metadata.MustGet(m, metadata.InstanceID)
	nodeId := fmt.Sprintf("%s:%s", nodeName, instanceId)

	d := &OSS{}
	d.endpoint = endpoint

	csiDriver := csicommon.NewCSIDriver(driverName, version.VERSION, nodeId)
	csiDriver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	})
	csiDriver.AddControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_UNKNOWN,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_READONLY,
	})

	d.driver = csiDriver

	cfg := options.MustGetRestConfig()
	clientset := kubernetes.NewForConfigOrDie(cfg)
	cnfsGetter := cnfsv1beta1.NewCNFSGetter(dynamic.NewForConfigOrDie(cfg))

	configmap, err := clientset.CoreV1().ConfigMaps("kube-system").Get(context.Background(), "csi-plugin", metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		log.Fatalf("failed to get configmap kube-system/csi-plugin: %v", err)
	}

	ossfsMounterFac := mounter.NewContainerizedFuseMounterFactory(mounter.NewFuseOssfs(configmap, m), clientset)

	if runAsController {
		d.controllerServer = &controllerServer{
			client:                  clientset,
			DefaultControllerServer: csicommon.NewDefaultControllerServer(d.driver),
		}
	} else {
		d.nodeServer = &nodeServer{
			DefaultNodeServer: csicommon.NewDefaultNodeServer(d.driver),
			nodeName:          nodeName,
			clientset:         clientset,
			cnfsGetter:        cnfsGetter,
			sharedPathLock:    utils.NewVolumeLocks(),
			ossfsMounterFac:   ossfsMounterFac,
			metadata:          m,
			rawMounter:        mountutils.New(""),
		}
	}

	return d
}

func (d *OSS) Run() {
	common.RunCSIServer(d.endpoint, csicommon.NewDefaultIdentityServer(d.driver), d.controllerServer, d.nodeServer)
}
