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

package nas

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/dadi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/nas/cloud"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/options"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/version"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
)

const (
	driverName = "nasplugin.csi.alibabacloud.com"
)

var (
	// GlobalConfigVar Global Config
	GlobalConfigVar GlobalConfig
)

// GlobalConfig save global values for plugin
type GlobalConfig struct {
	Region                  string
	NasTagEnable            bool
	ADControllerEnable      bool
	MetricEnable            bool
	NasFakeProvision        bool
	RunTimeClass            string
	NodeID                  string
	NodeIP                  string
	ClusterID               string
	LosetupEnable           bool
	NasPortCheck            bool
	KubeClient              *kubernetes.Clientset
	DynamicClient           dynamic.Interface
	NasClientFactory        *cloud.NasClientFactory
	EventRecorder           record.EventRecorder
	EnableDeletionFinalzier bool
}

// NAS the NAS object
type NAS struct {
	endpoint         string
	identityServer   *identityServer
	controllerServer *controllerServer
	nodeServer       *nodeServer
}

func NewDriver(nodeID, endpoint, serviceType string) *NAS {
	log.Infof("Driver: %v version: %v", driverName, version.VERSION)

	var d NAS
	d.endpoint = endpoint

	d.identityServer = newIdentityServer(driverName, version.VERSION)

	if serviceType == utils.ProvisionerService {
		// TODO
		cs, err := newControllerServer(nil)
		if err != nil {
			log.Fatalf("failed to init nas controller server: %v", err)
		}
		d.controllerServer = cs
	} else {
		d.nodeServer = newNodeServer()
	}
	return &d

	// csiDriver := csicommon.NewCSIDriver(driverName, version.VERSION, nodeID)
	// csiDriver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER})
	// csiDriver.AddControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{
	// 	csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
	// 	csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	// 	csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
	// })
	//
	// // Global Configs Set
	// GlobalConfigSet(serviceType)
	//
	// regionID := os.Getenv("REGION_ID")
	// if len(regionID) == 0 {
	// 	regionID, _ = utils.GetMetaData(RegionTag)
	// }
	// GlobalConfigVar.Region = regionID
	//
	// limit := os.Getenv("NAS_LIMIT_PERSECOND")
	// nasQps, err := strconv.Atoi(limit)
	// if err != nil {
	// 	log.Errorf("invalid NAS_LIMIT_PERSECOND %q", limit)
	// 	nasQps = 2
	// }
	// GlobalConfigVar.NasClientFactory = cloud.NewNasClientFactory(nasQps)
	// GlobalConfigVar.EventRecorder = utils.NewEventRecorder()
	// if enableDeletionFinalzier, err := strconv.ParseBool(os.Getenv("ENABLE_SUBPATH_DELETION_FINALZIER")); err == nil {
	// 	GlobalConfigVar.EnableDeletionFinalzier = enableDeletionFinalzier
	// } else {
	// 	GlobalConfigVar.EnableDeletionFinalzier = true
	// }
	//
	// d.driver = csiDriver
}

func (d *NAS) Run() {
	common.RunCSIServer(d.endpoint, d.identityServer, d.controllerServer, d.nodeServer)
}

// GlobalConfigSet set global config
func GlobalConfigSet(serviceType string) {
	// Global Configs Set
	cfg, err := clientcmd.BuildConfigFromFlags(options.MasterURL, options.Kubeconfig)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err.Error())
	}
	if qps := os.Getenv("KUBE_CLI_API_QPS"); qps != "" {
		if qpsi, err := strconv.Atoi(qps); err == nil {
			cfg.QPS = float32(qpsi)
		}
	}
	if burst := os.Getenv("KUBE_CLI_API_BURST"); burst != "" {
		if qpsi, err := strconv.Atoi(burst); err == nil {
			cfg.Burst = qpsi
		}
	}
	cfg.AcceptContentTypes = strings.Join([]string{runtime.ContentTypeProtobuf, runtime.ContentTypeJSON}, ",")
	cfg.ContentType = runtime.ContentTypeProtobuf
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}
	crdClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("NewControllerServer: Failed to create crd client: %v", err)
	}

	configMapName := "csi-plugin"
	isNasMetricEnable := false
	isNasFakeProvisioner := false

	configMap, err := kubeClient.CoreV1().ConfigMaps("kube-system").Get(context.Background(), configMapName, metav1.GetOptions{})
	if err != nil {
		log.Infof("Not found configmap named as csi-plugin under kube-system, with error: %v", err)
	} else {
		if value, ok := configMap.Data["nas-metric-enable"]; ok {
			if value == "enable" || value == "yes" || value == "true" {
				log.Infof("Nas Metric is enabled by configMap(%s).", value)
				isNasMetricEnable = true
			}
		}
		if value, ok := configMap.Data["nas-fake-provision"]; ok {
			if value == "enable" || value == "yes" || value == "true" {
				isNasFakeProvisioner = true
			}
		}

		_, ok1 := configMap.Data["cnfs-cache-properties"]
		_, ok2 := configMap.Data["nas-efc-cache"]
		if ok1 || ok2 {
			//start go write cluster nodeIP to /etc/hosts
			//format{["192.168.1.1:8800", "192.168.1.2:8801", "192.168.1.3:8802"]}
			//get service endpoint->format json->write /etc/hosts/dadi-endpoint.json
			if serviceType == utils.PluginService {
				go dadi.Run(kubeClient)
			}
		}
	}

	metricNasConf := os.Getenv(NasMetricByPlugin)
	if metricNasConf == "true" || metricNasConf == "yes" {
		isNasMetricEnable = true
	} else if metricNasConf == "false" || metricNasConf == "no" {
		isNasMetricEnable = false
	}

	nodeName := os.Getenv("KUBE_NODE_NAME")
	runtimeValue := "runc"
	nodeInfo, err := kubeClient.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		log.Errorf("Describe node %s with error: %s", nodeName, err.Error())
	} else {
		if value, ok := nodeInfo.Labels["alibabacloud.com/container-runtime"]; ok && strings.TrimSpace(value) == "Sandboxed-Container.runv" {
			if value, ok := nodeInfo.Labels["alibabacloud.com/container-runtime-version"]; ok && strings.HasPrefix(strings.TrimSpace(value), "1.") {
				runtimeValue = MixRunTimeMode
			}
		}
		log.Infof("Describe node %s and set RunTimeClass to %s", nodeName, runtimeValue)
	}

	if nodeInfo != nil {
		for _, address := range nodeInfo.Status.Addresses {
			if address.Type == "InternalIP" {
				log.Infof("Node InternalIP is: %s", address.Address)
				GlobalConfigVar.NodeIP = address.Address
			}
		}
	}

	GlobalConfigVar.LosetupEnable = false
	losetupEn := os.Getenv("NAS_LOSETUP_ENABLE")
	if losetupEn == "true" || losetupEn == "yes" {
		GlobalConfigVar.LosetupEnable = true
	}

	if GlobalConfigVar.LosetupEnable && GlobalConfigVar.NodeIP == "" {
		log.Fatal("Init GlobalConfigVar with NodeIP Empty, Nas losetup feature may be useless")
	}
	clustID := os.Getenv("CLUSTER_ID")

	doNfsPortCheck := true
	nasCheck := os.Getenv("NAS_PORT_CHECK")
	if nasCheck == "no" || nasCheck == "false" {
		doNfsPortCheck = false
	}

	GlobalConfigVar.KubeClient = kubeClient
	GlobalConfigVar.DynamicClient = crdClient
	GlobalConfigVar.MetricEnable = isNasMetricEnable
	GlobalConfigVar.RunTimeClass = runtimeValue
	GlobalConfigVar.NodeID = nodeName
	GlobalConfigVar.ClusterID = clustID
	GlobalConfigVar.NasFakeProvision = isNasFakeProvisioner
	GlobalConfigVar.NasPortCheck = doNfsPortCheck

	log.Infof("NAS Global Config: %v", GlobalConfigVar)
}
