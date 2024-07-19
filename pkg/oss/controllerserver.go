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

package oss

import (
	"context"
	"fmt"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	csicommon "github.com/kubernetes-csi/drivers/pkg/csi-common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	cnfsv1beta1 "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cnfs/v1beta1"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// controller server try to create/delete volumes
type controllerServer struct {
	client kubernetes.Interface
	*csicommon.DefaultControllerServer
	cnfsGetter      cnfsv1beta1.CNFSGetter
	metadata        metadata.MetadataProvider
	ossfsMounterFac *mounter.ContainerizedFuseMounterFactory
}

func getOssVolumeOptions(req *csi.CreateVolumeRequest) *Options {
	ossVolArgs := &Options{}
	volOptions := req.GetParameters()
	secret := req.GetSecrets()
	volCaps := req.GetVolumeCapabilities()
	ossVolArgs.Path = "/"
	for k, v := range volOptions {
		key := strings.TrimSpace(strings.ToLower(k))
		value := strings.TrimSpace(v)
		if key == "bucket" {
			ossVolArgs.Bucket = value
		} else if key == "url" {
			ossVolArgs.URL = value
		} else if key == "otheropts" {
			ossVolArgs.OtherOpts = value
		} else if key == "path" {
			ossVolArgs.Path = value
		} else if key == "usesharedpath" && value == "true" {
			ossVolArgs.UseSharedPath = true
		} else if key == "authtype" {
			ossVolArgs.AuthType = value
		} else if key == "rolename" {
			ossVolArgs.RoleName = value
		} else if key == "rolearn" {
			ossVolArgs.RoleArn = strings.TrimSpace(value)
		} else if key == "oidcproviderarn" {
			ossVolArgs.OidcProviderArn = strings.TrimSpace(value)
		} else if key == "serviceaccountname" {
			ossVolArgs.ServiceAccountName = strings.TrimSpace(value)
		} else if key == "secretproviderclass" {
			ossVolArgs.SecretProviderClass = value
		} else if key == "encrypted" {
			ossVolArgs.Encrypted = value
		} else if key == "kmsKeyId" {
			ossVolArgs.KmsKeyId = value
		}
	}
	for k, v := range secret {
		key := strings.TrimSpace(strings.ToLower(k))
		value := strings.TrimSpace(v)
		if key == "akid" {
			ossVolArgs.AkID = value
		} else if key == "aksecret" {
			ossVolArgs.AkSecret = value
		}
	}
	ossVolArgs.ReadOnly = true
	for _, c := range volCaps {
		switch c.AccessMode.GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER, csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER:
			ossVolArgs.ReadOnly = false
		}
	}
	return ossVolArgs
}
func validateCreateVolumeRequest(req *csi.CreateVolumeRequest) error {
	log.Infof("Starting oss validate create volume request: %s, %v", req.Name, req)
	valid, err := utils.CheckRequestArgs(req.GetParameters())
	if !valid {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}

	return nil
}

// provisioner: create/delete oss volume
func (cs *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if err := validateCreateVolumeRequest(req); err != nil {
		return nil, err
	}
	ossVol := getOssVolumeOptions(req)
	csiTargetVolume := &csi.Volume{}
	volumeContext := req.GetParameters()
	if volumeContext == nil {
		volumeContext = map[string]string{}
	}
	volumeContext["path"] = ossVol.Path
	volSizeBytes := int64(req.GetCapacityRange().GetRequiredBytes())
	csiTargetVolume = &csi.Volume{
		VolumeId:      req.Name,
		CapacityBytes: int64(volSizeBytes),
		VolumeContext: volumeContext,
	}

	log.Infof("Provision oss volume is successfully: %s,pvName: %v", req.Name, csiTargetVolume)
	return &csi.CreateVolumeResponse{Volume: csiTargetVolume}, nil

}

// call nas api to delete oss volume
func (cs *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	log.Infof("DeleteVolume: Starting deleting volume %s", req.GetVolumeId())
	_, err := cs.client.CoreV1().PersistentVolumes().Get(context.Background(), req.VolumeId, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("DeleteVolume: Get volume %s is failed, err: %s", req.VolumeId, err.Error())
	}
	log.Infof("Delete volume %s is successfully", req.VolumeId)
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *controllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	log.Infof("ControllerUnpublishVolume: volume %s on node %s", req.VolumeId, req.NodeId)
	nodeName, instanceId, _ := strings.Cut(req.NodeId, ":")
	if nodeName == "" || instanceId == "" {
		log.Warnf("ControllerUnpublishVolume: skip %s for unexpected nodeId: %q", req.VolumeId, req.NodeId)
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}

	mountPath := getControllerPublishMountPath(req.VolumeId)
	err := cs.ossfsMounterFac.NewFuseMounter(&mounter.FusePodContext{
		Context:   ctx,
		Namespace: "ack-csi-fuse",
		NodeName:  nodeName,
		VolumeId:  req.VolumeId,
	}, false).Unmount(mountPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount: %v", err)
	}

	secretName := getControllerPublishSecretName(req.VolumeId, nodeName)
	err = cs.client.CoreV1().Secrets("ack-csi-fuse").Delete(ctx, secretName, metav1.DeleteOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, status.Errorf(codes.Internal, "failed to delete secret %s: %v", secretName, err)
		}
	} else {
		log.Infof("ControllerPublishVolume: deleted secret %s", secretName)
	}

	log.Infof("ControllerUnpublishVolume: successfully unpublished volume %s on node %s", req.VolumeId, req.NodeId)
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (cs *controllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	log.Infof("ControllerPublishVolume: volume %s on node %s", req.VolumeId, req.NodeId)
	// TODO: skip controller publish for virtual kubelet nodes
	nodeName, instanceId, _ := strings.Cut(req.NodeId, ":")
	if nodeName == "" || instanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "unexpected nodeId")
	}

	opts := parseOptions(req)
	if err := setCNFSOptions(ctx, cs.cnfsGetter, opts); err != nil {
		return nil, err
	}
	// check parameters
	if err := checkOssOptions(opts); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}
	if opts.directAssigned {
		log.Infof("ControllerPublishVolume: skip DirectVolume: %s", req.VolumeId)
		return &csi.ControllerPublishVolumeResponse{}, nil
	}
	if !opts.UseSharedPath {
		return nil, status.Errorf(codes.InvalidArgument, "useSharedPath=false no longer supported")
	}

	authCfg := &mounter.AuthConfig{AuthType: opts.AuthType, SecretProviderClassName: opts.SecretProviderClass}
	if opts.AuthType == mounter.AuthTypeRRSA {
		rrsaCfg, err := getRRSAConfig(opts, cs.metadata)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Get RoleArn and OidcProviderArn for RRSA error: %v", err)
		}
		authCfg.RrsaConfig = rrsaCfg
	}

	mountOptions, err := opts.MakeMountOptions(req.VolumeCapability)
	if err != nil {
		return nil, err
	}

	regionID, _ := cs.metadata.Get(metadata.RegionID)
	switch opts.AuthType {
	case mounter.AuthTypeSTS:
		// TODO: add sts mount option
	case mounter.AuthTypeRRSA:
		if regionID == "" {
			mountOptions = append(mountOptions, "rrsa_endpoint=https://sts.aliyuncs.com")
		} else {
			mountOptions = append(mountOptions, fmt.Sprintf("rrsa_endpoint=https://sts-vpc.%s.aliyuncs.com", regionID))
		}
	case mounter.AuthTypeCSS:
	default:
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getControllerPublishSecretName(req.VolumeId, nodeName),
				Namespace: "ack-csi-fuse",
			},
			StringData: req.Secrets,
		}
		client := cs.client.CoreV1().Secrets("ack-csi-fuse")
		_, err := client.Create(ctx, &secret, metav1.CreateOptions{})
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				_, err = client.Update(ctx, &secret, metav1.UpdateOptions{})
				if err != nil {
					return nil, status.Errorf(codes.Internal, "failed to update secret: %v", err)
				}
				log.Infof("ControllerPublishVolume: updated secret %s", secret.Name)
			} else {
				return nil, status.Errorf(codes.Internal, "failed to create secret: %v", err)
			}
		} else {
			log.Infof("ControllerPublishVolume: created secret %s", secret.Name)
		}

		log.Infof("ControllerPublishVolume: applied secret for volume %s", req.VolumeId)
	}

	if regionID == "" {
		log.Warnf("ControllerPublishVolume: failed to get region id from both env and metadata, use original URL: %s", opts.URL)
	} else if url, done := setNetworkType(opts.URL, regionID); done {
		log.Warnf("ControllerPublishVolume: setNetworkType: modified URL from %s to %s", opts.URL, url)
		opts.URL = url
	}
	if url, done := setTransmissionProtocol(opts.URL); done {
		log.Warnf("ControllerPublishVolume: setTransmissionProtocol: modified URL from %s to %s", opts.URL, url)
		opts.URL = url
	}

	mountPath := getControllerPublishMountPath(req.VolumeId)
	err = cs.ossfsMounterFac.NewFuseMounter(&mounter.FusePodContext{
		Context:    ctx,
		Namespace:  "ack-csi-fuse",
		NodeName:   nodeName,
		VolumeId:   req.VolumeId,
		AuthConfig: authCfg,
	}, false).Mount(fmt.Sprintf("%s:%s", opts.Bucket, opts.Path), mountPath, opts.FuseType, mountOptions)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount: %v", err)
	}

	log.Infof("ControllerPublishVolume: successfully published volume %s on node %s", req.VolumeId, req.NodeId)
	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			"controllerPublishPath": mountPath,
		},
	}, nil
}

func (cs *controllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	log.Infof("CreateSnapshot is called, do nothing now")
	return &csi.CreateSnapshotResponse{}, nil
}

func (cs *controllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	log.Infof("DeleteSnapshot is called, do nothing now")
	return &csi.DeleteSnapshotResponse{}, nil
}

func (cs *controllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest,
) (*csi.ControllerExpandVolumeResponse, error) {
	log.Infof("ControllerExpandVolume is called, do nothing now")
	return &csi.ControllerExpandVolumeResponse{}, nil
}

func (cs *controllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	log.Info("1")
	return cs.DefaultControllerServer.ControllerGetCapabilities(ctx, req)
}
