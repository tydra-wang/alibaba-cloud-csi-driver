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
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	csicommon "github.com/kubernetes-csi/drivers/pkg/csi-common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	cnfsv1beta1 "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cnfs/v1beta1"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/kubernetes"
	mountutils "k8s.io/mount-utils"
)

type nodeServer struct {
	metadata metadata.MetadataProvider
	*csicommon.DefaultNodeServer
	nodeName        string
	clientset       kubernetes.Interface
	cnfsGetter      cnfsv1beta1.CNFSGetter
	sharedPathLock  *utils.VolumeLocks
	ossfsMounterFac *mounter.ContainerizedFuseMounterFactory
	rawMounter      mountutils.Interface
}

// Options contains options for target oss
type Options struct {
	directAssigned      bool
	CNFSName            string
	Bucket              string `json:"bucket"`
	URL                 string `json:"url"`
	OtherOpts           string `json:"otherOpts"`
	AkID                string `json:"akId"`
	AkSecret            string `json:"akSecret"`
	Path                string `json:"path"`
	UseSharedPath       bool   `json:"useSharedPath"`
	AuthType            string `json:"authType"`
	RoleName            string `json:"roleName"`
	RoleArn             string `json:"roleArn"`
	OidcProviderArn     string `json:"oidcProviderArn"`
	ServiceAccountName  string `json:"serviceAccountName"`
	SecretProviderClass string `json:"secretProviderClass"`
	FuseType            string `json:"fuseType"`
	MetricsTop          string `json:"metricsTop"`
	ReadOnly            bool   `json:"readOnly"`
	Encrypted           string `json:"encrypted"`
	KmsKeyId            string `json:"kmsKeyId"`
}

const (
	// OssfsCredentialFile is the path of oss ak credential file
	OssfsCredentialFile = "/host/etc/passwd-ossfs"
	// AkID is Ak ID
	AkID = "akId"
	// AkSecret is Ak Secret
	AkSecret = "akSecret"
	// OssFsType is the oss filesystem type
	OssFsType = "ossfs"
	// metricsPathPrefix
	metricsPathPrefix = "/host/var/run/ossfs/"
	// defaultMetricsTop
	defaultMetricsTop = "10"
	// fuseServieAccountName
	fuseServieAccountName = "csi-fuse-ossfs"
)

const (
	EncryptedTypeKms    = "kms"
	EncryptedTypeAes256 = "aes256"
)

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
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

func validateNodePublishVolumeRequest(req *csi.NodePublishVolumeRequest) error {
	valid, err := utils.CheckRequest(req.GetVolumeContext(), req.GetTargetPath())
	if !valid {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}
	return nil
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	log.Infof("NodePublishVolume:: Starting Mount volume: %s mount with req: %+v", req.VolumeId, req)
	mountPath := req.GetTargetPath()
	if err := validateNodePublishVolumeRequest(req); err != nil {
		return nil, err
	}

	notMnt, err := ns.rawMounter.IsLikelyNotMountPoint(mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(mountPath, os.ModePerm); err != nil {
				return nil, status.Errorf(codes.Internal, "mkdir: %v", err)
			}
			notMnt = true
		} else {
			return nil, status.Errorf(codes.Internal, "check mountpoint: %v", err)
		}
	}
	if !notMnt {
		log.Infof("NodePublishVolume: %s already mounted", mountPath)
	}

	controllerPublishPath := req.PublishContext["controllerPublishPath"]
	// TODO: metrics file
	if controllerPublishPath != "" {
		log.Infof("NodePublishVolume: controller publish path detected: %s", controllerPublishPath)
		notMnt, err := ns.rawMounter.IsLikelyNotMountPoint(controllerPublishPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "check controller publish mount path: %v", err)
		}
		if notMnt {
			return nil, status.Errorf(codes.Internal, "controller publish mount path not monuted: %v", controllerPublishPath)
		}
		if err := ns.rawMounter.Mount(controllerPublishPath, mountPath, "", []string{"bind"}); err != nil {
			return nil, status.Errorf(codes.Internal, "bind mount: %v", err)
		}
		log.Infof("NodePublishVolume: bind mounted: %s", mountPath)

		return &csi.NodePublishVolumeResponse{}, nil
	}

	opt := parseOptions(req)
	if err := setCNFSOptions(ctx, ns.cnfsGetter, opt); err != nil {
		return nil, err
	}
	// check parameters
	if err := checkOssOptions(opt); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	if opt.directAssigned {
		return ns.publishDirectVolume(ctx, req, opt)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

// Check oss options
func checkOssOptions(opt *Options) error {
	if opt.FuseType != OssFsType {
		return errors.New("only ossfs fuse type supported")
	}

	if opt.URL == "" || opt.Bucket == "" {
		return errors.New("Oss parameters error: Url/Bucket empty")
	}

	if !strings.HasPrefix(opt.Path, "/") {
		return errors.New("Oss path error: start with " + opt.Path + ", should start with /")
	}

	switch opt.AuthType {
	case mounter.AuthTypeSTS:
	case mounter.AuthTypeRRSA:
		if err := checkRRSAParams(opt); err != nil {
			return err
		}
	case mounter.AuthTypeCSS:
		if opt.SecretProviderClass == "" {
			return errors.New("Oss parameters error: use CsiSecretStore but secretProviderClass is empty")
		}
	default:
		// if not input ak from user, use the default ak value
		if opt.AkID == "" || opt.AkSecret == "" {
			ac := utils.GetEnvAK()
			opt.AkID = ac.AccessKeyID
			opt.AkSecret = ac.AccessKeySecret
		}
		if opt.AkID == "" || opt.AkSecret == "" {
			return errors.New("Oss parameters error: AK and authType are both empty or invalid")
		}
	}

	if opt.Encrypted != "" && opt.Encrypted != EncryptedTypeKms && opt.Encrypted != EncryptedTypeAes256 {
		return errors.New("Oss encrypted error: invalid SSE encrypted type")
	}

	return nil
}

func validateNodeUnpublishVolumeRequest(req *csi.NodeUnpublishVolumeRequest) error {
	valid, err := utils.ValidatePath(req.GetTargetPath())
	if !valid {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}
	return nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	log.Infof("NodeUnpublishVolume: Starting Umount OSS: %s mount with req: %+v", req.TargetPath, req)
	mountPoint := req.TargetPath
	err := validateNodeUnpublishVolumeRequest(req)
	if err != nil {
		return nil, err
	}
	if isDirectVolumePath(mountPoint) {
		return ns.unPublishDirectVolume(ctx, req)
	}
	err = mountutils.CleanupMountWithForce(mountPoint, ns.rawMounter.(mountutils.MounterForceUnmounter), true, time.Minute)
	if err != nil {
		log.Errorf("NodeUnpublishVolume: failed to unmount %q: %v", mountPoint, err)
		return nil, status.Errorf(codes.Internal, "failed to unmount target %q: %v", mountPoint, err)
	}
	log.Infof("NodeUnpublishVolume: Umount OSS Successful: %s", mountPoint)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(
	ctx context.Context,
	req *csi.NodeStageVolumeRequest) (
	*csi.NodeStageVolumeResponse, error) {
	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(
	ctx context.Context,
	req *csi.NodeUnstageVolumeRequest) (
	*csi.NodeUnstageVolumeResponse, error) {
	log.Infof("NodeUnstageVolume: starting to unmount volume, volumeId: %s, target: %v", req.VolumeId, req.StagingTargetPath)
	// unmount for sharedPath
	mountpoint := GetGlobalMountPath(req.VolumeId)
	err := mountutils.CleanupMountWithForce(mountpoint, ns.rawMounter.(mountutils.MounterForceUnmounter), true, time.Minute)
	if err != nil {
		log.Errorf("NodeUnstageVolume: failed to unmount %q: %v", mountpoint, err)
		return nil, status.Errorf(codes.Internal, "failed to unmount target %q: %v", mountpoint, err)
	}
	log.Infof("NodeUnstageVolume: umount OSS Successful: %s", mountpoint)
	err = mounter.CleanupOssfsCredentialSecret(ctx, ns.clientset, ns.nodeName, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cleanup ossfs credential secret: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (
	*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

type publishRequest interface {
	GetVolumeCapability() *csi.VolumeCapability
	GetReadonly() bool
	GetVolumeContext() map[string]string
	GetSecrets() map[string]string
}

func parseOptions(req publishRequest) *Options {
	opts := &Options{}
	opts.UseSharedPath = true
	opts.FuseType = OssFsType
	for key, value := range req.GetVolumeContext() {
		key = strings.ToLower(key)
		if key == "bucket" {
			opts.Bucket = strings.TrimSpace(value)
		} else if key == "url" {
			opts.URL = strings.TrimSpace(value)
		} else if key == "otheropts" {
			opts.OtherOpts = strings.TrimSpace(value)
		} else if key == "akid" {
			opts.AkID = strings.TrimSpace(value)
		} else if key == "aksecret" {
			opts.AkSecret = strings.TrimSpace(value)
		} else if key == "path" {
			if v := strings.TrimSpace(value); v == "" {
				opts.Path = "/"
			} else {
				opts.Path = v
			}
		} else if key == "usesharedpath" {
			if useSharedPath, err := strconv.ParseBool(value); err == nil {
				opts.UseSharedPath = useSharedPath
			} else {
				log.Warnf("invalid useSharedPath: %q", value)
			}
		} else if key == "authtype" {
			opts.AuthType = strings.ToLower(strings.TrimSpace(value))
		} else if key == "rolename" {
			opts.RoleName = strings.TrimSpace(value)
		} else if key == "rolearn" {
			opts.RoleArn = strings.TrimSpace(value)
		} else if key == "oidcproviderarn" {
			opts.OidcProviderArn = strings.TrimSpace(value)
		} else if key == "serviceaccountname" {
			opts.ServiceAccountName = strings.TrimSpace(value)
		} else if key == "secretproviderclass" {
			opts.SecretProviderClass = strings.TrimSpace(value)
		} else if key == "fusetype" {
			opts.FuseType = strings.ToLower(strings.TrimSpace(value))
		} else if key == "metricstop" {
			opts.MetricsTop = strings.ToLower(strings.TrimSpace(value))
		} else if key == "containernetworkfilesystem" {
			opts.CNFSName = value
		} else if key == optDirectAssigned {
			opts.directAssigned, _ = strconv.ParseBool(strings.TrimSpace(value))
		} else if key == "encrypted" {
			opts.Encrypted = strings.ToLower(strings.TrimSpace(value))
		} else if key == "kmskeyid" {
			opts.KmsKeyId = value
		}
	}
	// support set ak by secret
	if opts.AkID == "" || opts.AkSecret == "" {
		secrets := req.GetSecrets()
		opts.AkID, opts.AkSecret = secrets[AkID], secrets[AkSecret]
	}
	if req.GetReadonly() {
		opts.ReadOnly = true
	} else if capability := req.GetVolumeCapability(); capability != nil {
		switch capability.AccessMode.GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER, csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER:
			opts.ReadOnly = false
		default:
			opts.ReadOnly = true
		}
	}

	if opts.Path == "" {
		opts.Path = "/"
	}
	return opts
}

func setCNFSOptions(ctx context.Context, cnfsGetter cnfsv1beta1.CNFSGetter, opts *Options) error {
	if opts.CNFSName == "" {
		return nil
	}
	cnfs, err := cnfsGetter.GetCNFS(ctx, opts.CNFSName)
	if err != nil {
		return err
	}
	if cnfs.Status.FsAttributes.EndPoint == nil {
		return fmt.Errorf("missing endpoint in status of CNFS %s", opts.CNFSName)
	}
	opts.Bucket = cnfs.Status.FsAttributes.BucketName
	opts.URL = cnfs.Status.FsAttributes.EndPoint.Internal
	return nil
}

func (o *Options) MakeMountOptions(volumeCapability *csi.VolumeCapability) ([]string, error) {
	var mountOptions []string
	if volumeCapability != nil && volumeCapability.GetMount() != nil {
		mountOptions = volumeCapability.GetMount().MountFlags
	}

	mountOptions, err := parseOtherOpts(o.OtherOpts)
	if err != nil {
		return nil, err
	}
	switch o.Encrypted {
	case EncryptedTypeAes256:
		mountOptions = append(mountOptions, "use_sse")
	case EncryptedTypeKms:
		if o.KmsKeyId == "" {
			mountOptions = append(mountOptions, "use_sse=kmsid")
		} else {
			mountOptions = append(mountOptions, fmt.Sprintf("use_sse=kmsid:%s", o.KmsKeyId))
		}
	}
	if o.ReadOnly {
		mountOptions = append(mountOptions, "ro")
	}
	// set use_metrics to enabled monitoring by default
	if features.FunctionalMutableFeatureGate.Enabled(features.UpdatedOssfsVersion) {
		mountOptions = append(mountOptions, "use_metrics")
	}
	if o.MetricsTop != "" {
		mountOptions = append(mountOptions, fmt.Sprintf("metrics_top=%s", o.MetricsTop))
	}

	mountOptions = append(mountOptions, fmt.Sprintf("url=%s", o.URL))
	return mountOptions, nil
}
