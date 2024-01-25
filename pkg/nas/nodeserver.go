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
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cnfs/v1beta1"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/dadi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/losetup"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/nas/internal"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mountutils "k8s.io/mount-utils"
)

type nodeServer struct {
	config  *internal.NodeConfig
	mounter mountutils.Interface
	locks   *utils.VolumeLocks
}

func newNodeServer(config *internal.NodeConfig) *nodeServer {
	// configure efc dadi service discovery
	if config.EnableEFCCache {
		go dadi.Run(config.KubeClient)
	}
	if err := checkSystemNasConfig(); err != nil {
		log.Errorf("failed to config /proc/sys/sunrpc/tcp_slot_table_entries: %v", err)
	}
	return &nodeServer{
		config:  config,
		mounter: NewNasMounter(),
		locks:   utils.NewVolumeLocks(),
	}
}

// Options struct definition
type Options struct {
	Server        string `json:"server"`
	Accesspoint   string `json:"accesspoint"`
	Path          string `json:"path"`
	Vers          string `json:"vers"`
	Mode          string `json:"mode"`
	ModeType      string `json:"modeType"`
	Options       string `json:"options"`
	MountType     string `json:"mountType"`
	LoopImageSize int    `json:"loopImageSize"`
	LoopLock      string `json:"loopLock"`
	MountProtocol string `json:"mountProtocol"`
	ClientType    string `json:"clientType"`
	FSType        string `json:"fsType"`
}

// RunvNasOptions struct definition
type RunvNasOptions struct {
	Server     string `json:"server"`
	Path       string `json:"path"`
	Vers       string `json:"vers"`
	Mode       string `json:"mode"`
	ModeType   string `json:"modeType"`
	Options    string `json:"options"`
	RunTime    string `json:"runtime"`
	MountFile  string `json:"mountfile"`
	VolumeType string `json:"volumeType"`
}

const (
	// NasTempMntPath used for create sub directory
	NasTempMntPath = "/mnt/acs_mnt/k8s_nas/temp"
	// NasPortnum is nas port
	NasPortnum = "2049"
	// NasMntPoint tag
	NasMntPoint = "/mnt/nasplugin.alibabacloud.com"
	// MountProtocolNFS common nfs protocol
	MountProtocolNFS = "nfs"
	// MountProtocolEFC common efc protocol
	MountProtocolEFC = "efc"
	// MountProtocolCPFS common cpfs protocol
	MountProtocolCPFS = "cpfs"
	// MountProtocolCPFSNFS cpfs-nfs protocol
	MountProtocolCPFSNFS = "cpfs-nfs"
	// MountProtocolCPFSNative cpfs-native protocol
	MountProtocolCPFSNative = "cpfs-native"
	// MountProtocolAliNas alinas protocol
	MountProtocolAliNas = "alinas"
	// MountProtocolTag tag
	MountProtocolTag = "mountProtocol"
	// metricsPathPrefix
	metricsPathPrefix = "/host/var/run/efc/"
	//EFCClient
	EFCClient = "efcclient"
	//NFSClient
	NFSClient = "nfsclient"
	//NativeClient
	NativeClient = "nativeclient"
)

func validateNodePublishVolumeRequest(req *csi.NodePublishVolumeRequest) error {
	valid, err := utils.CheckRequest(req.GetVolumeContext(), req.GetTargetPath())
	if !valid {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}
	return nil
}

func DetermineClientTypeAndMountProtocol(cnfs *v1beta1.ContainerNetworkFileSystem, opt *Options) error {
	useEaClient := cnfs.Status.FsAttributes.UseElasticAccelerationClient
	useClient := cnfs.Status.FsAttributes.UseClient
	if len(useClient) == 0 && useEaClient == "true" {
		useClient = "EFCClient"
	} else if len(useClient) == 0 {
		useClient = "NFSClient"
	}
	opt.ClientType = strings.ToLower(useClient)
	opt.FSType = strings.ToLower(cnfs.Status.FsAttributes.FilesystemType)
	switch opt.FSType {
	case "standard":
		opt.Server = cnfs.Status.FsAttributes.Server
		switch opt.ClientType {
		case EFCClient:
			opt.MountProtocol = MountProtocolEFC
		case NFSClient:
			opt.MountProtocol = MountProtocolNFS
		default:
			return errors.New("Don't Support useClient:" + useClient)
		}
	case "cpfs":
		switch opt.ClientType {
		case EFCClient:
			opt.Server = cnfs.Status.FsAttributes.ProtocolServer
			opt.MountProtocol = MountProtocolEFC
		case NFSClient:
			opt.Server = cnfs.Status.FsAttributes.ProtocolServer
			opt.MountProtocol = MountProtocolCPFSNFS
		case NativeClient:
			opt.Server = cnfs.Status.FsAttributes.Server
			opt.MountProtocol = MountProtocolCPFS
		default:
			return errors.New("Don't Support useClient:" + useClient)
		}
	default:
		return errors.New("Don't Support Storage type:" + opt.FSType)
	}
	return nil
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	log.Infof("NodePublishVolume:: Nas Volume %s mount with req: %+v", req.VolumeId, req)
	mountPath := req.GetTargetPath()
	if err := validateNodePublishVolumeRequest(req); err != nil {
		return nil, err
	}

	if !ns.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for %s", req.VolumeId)
	}
	defer ns.locks.Release(req.VolumeId)
	notMounted, err := ns.mounter.IsLikelyNotMountPoint(mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(mountPath, os.ModePerm); err != nil {
				log.Errorf("NodePublishVolume: mkdir %s: %v", mountPath, err)
				return nil, status.Error(codes.Internal, err.Error())
			}
			notMounted = true
		} else {
			log.Errorf("NodePublishVolume: check mountpoint %s: %v", mountPath, err)
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if !notMounted {
		log.Infof("NodePublishVolume: %s already mounted", mountPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// parse parameters
	opt := &Options{}
	var cnfsName string
	for key, value := range req.VolumeContext {
		switch strings.ToLower(key) {
		case "server":
			opt.Server = value
		case "accesspoint":
			opt.Accesspoint = value
		case "path":
			opt.Path = value
		case "vers":
			opt.Vers = value
		case "mode":
			opt.Mode = value
		case "options":
			opt.Options = value
		case "modetype":
			opt.ModeType = value
		case "mounttype":
			opt.MountType = value
		case "looplock":
			opt.LoopLock = value
		case "loopimagesize":
			size, err := strconv.Atoi(value)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid loopImageSize: %q", value)
			}
			opt.LoopImageSize = size
		case "containernetworkfilesystem":
			cnfsName = value
		case "mountprotocol":
			opt.MountProtocol = strings.TrimSpace(value)
		}
	}

	if cnfsName != "" {
		cnfs, err := ns.config.CNFSGetter.GetCNFS(ctx, cnfsName)
		if err != nil {
			return nil, err
		}
		err = DetermineClientTypeAndMountProtocol(cnfs, opt)
		if err != nil {
			return nil, err
		}
	}
	if opt.Server == "" || opt.Accesspoint == "" {
		return nil, status.Error(codes.InvalidArgument, "failed to determine nas mount target or accesspoint")
	}

	if opt.LoopLock != "false" {
		opt.LoopLock = "true"
	}
	if opt.MountProtocol == "" {
		opt.MountProtocol = MountProtocolNFS
	} else if opt.MountProtocol == MountProtocolCPFSNative {
		opt.FSType = "cpfs"
		opt.MountProtocol = MountProtocolCPFS
		opt.ClientType = NativeClient
	}

	// version/options used first in mountOptions
	if req.VolumeCapability != nil && req.VolumeCapability.GetMount() != nil {
		mntOptions := req.VolumeCapability.GetMount().MountFlags
		parseVers, parseOptions := ParseMountFlags(mntOptions)
		if parseVers != "" {
			if opt.Vers != "" {
				log.Warnf("NodePublishVolume: Vers(%s) (in volumeAttributes) is ignored as Vers(%s) also configured in mountOptions", opt.Vers, parseVers)
			}
			opt.Vers = parseVers
		}
		if parseOptions != "" {
			if opt.Options != "" {
				log.Warnf("NodePublishVolume: Options(%s) (in volumeAttributes) is ignored as Options(%s) also configured in mountOptions", opt.Options, parseOptions)
			}
			opt.Options = parseOptions
		}
	}

	// running in runc/runv mode
	if ns.config.EnableMixRuntime {
		if runtime, err := utils.GetPodRunTime(req, ns.config.KubeClient); err != nil {
			return nil, status.Errorf(codes.Internal, "NodePublishVolume: cannot get pod runtime: %v", err)
		} else if runtime == utils.RunvRunTimeTag {
			fileName := filepath.Join(mountPath, utils.CsiPluginRunTimeFlagFile)
			runvOptions := RunvNasOptions{}
			runvOptions.Options = opt.Options
			runvOptions.Server = opt.Server
			runvOptions.ModeType = opt.ModeType
			runvOptions.Mode = opt.Mode
			runvOptions.Vers = opt.Vers
			runvOptions.Path = opt.Path
			runvOptions.RunTime = "runv"
			runvOptions.VolumeType = "nfs"
			runvOptions.MountFile = fileName
			if err := utils.WriteJSONFile(runvOptions, fileName); err != nil {
				return nil, errors.New("NodePublishVolume: Write Json File error: " + err.Error())
			}
			log.Infof("Nas(Kata), Write Nfs Options to File Successful: %s", fileName)
			return &csi.NodePublishVolumeResponse{}, nil
		}
	}

	// check network connection
	if ns.config.EnablePortCheck && opt.Server != "" && opt.MountType != SkipMountType &&
		(opt.MountProtocol == MountProtocolNFS || opt.MountProtocol == MountProtocolAliNas) {
		conn, err := net.DialTimeout("tcp", opt.Server+":"+NasPortnum, time.Second*time.Duration(30))
		if err != nil {
			log.Errorf("NAS: Cannot connect to nas host: %s", opt.Server)
			return nil, errors.New("NAS: Cannot connect to nas host: " + opt.Server)
		}
		defer conn.Close()
	}

	if opt.Path == "" {
		opt.Path = "/"
	}
	// if path end with /, remove /;
	if opt.Path != "/" && strings.HasSuffix(opt.Path, "/") {
		opt.Path = opt.Path[0 : len(opt.Path)-1]
	}

	if opt.Path == "/" && opt.Mode != "" {
		return nil, errors.New("NAS: root directory cannot set mode: " + opt.Mode)
	}
	if !strings.HasPrefix(opt.Path, "/") {
		return nil, errors.New("the path format is illegal")
	}
	if opt.Vers == "" || opt.Vers == "3.0" {
		opt.Vers = "3"
	} else if opt.Vers == "4" {
		opt.Vers = "4.0"
	}

	if strings.Contains(opt.Server, "extreme.nas.aliyuncs.com") {
		if opt.Vers != "3" {
			return nil, errors.New("Extreme nas only support nfs v3 " + opt.Server)
		}
	}
	// check options, config defaults for aliyun nas
	if opt.Options == "" {
		if opt.Vers == "3" {
			opt.Options = "nolock,proto=tcp,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport"
		} else {
			opt.Options = "noresvport"
		}
	} else if strings.ToLower(opt.Options) == "none" {
		opt.Options = ""
	}

	// Create Mount Path
	if err := utils.CreateDest(mountPath); err != nil {
		return nil, errors.New("Create mount path is failed, mountPath: " + mountPath + ", err:" + err.Error())
	}

	// if volume set mountType as skipmount;
	if opt.MountType == SkipMountType {
		err := ns.mounter.Mount("tmpfs", mountPath, "tmpfs", []string{"size=1m"})
		if err != nil {
			log.Errorf("NAS: Mount volume(%s) path as tmpfs with err: %v", req.VolumeId, err.Error())
			return nil, status.Error(codes.Internal, "NAS: Mount as tmpfs volume with err"+err.Error())
		}
		saveVolumeData(opt, mountPath)
		log.Infof("NodePublishVolume:: Volume %s is Skip Mount type, just save the metadata: %s", req.VolumeId, mountPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// do losetup nas logical
	if ns.config.EnableLosetup && opt.MountType == LosetupType {
		if err := ns.mountLosetupPv(mountPath, opt, req.VolumeId); err != nil {
			log.Errorf("NodePublishVolume: mount losetup volume(%s) error %s", req.VolumeId, err.Error())
			return nil, errors.New("NodePublishVolume, mount Losetup volume error with: " + err.Error())
		}
		log.Infof("NodePublishVolume: nas losetup volume successful %s", req.VolumeId)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Do mount
	podUID := req.VolumeContext["csi.storage.k8s.io/pod.uid"]
	if podUID == "" {
		log.Errorf("Volume(%s) Cannot get poduid and cannot set volume limit", req.VolumeId)
		return nil, errors.New("Cannot get poduid and cannot set volume limit: " + req.VolumeId)
	}
	//mount nas client
	if err := doMount(ns.mounter, opt, mountPath, req.VolumeId, podUID); err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}
	if opt.MountProtocol == "efc" {
		if strings.Contains(opt.Server, ".nas.aliyuncs.com") {
			fsID := GetFsIDByNasServer(opt.Server)
			if len(fsID) != 0 {
				utils.WriteMetricsInfo(metricsPathPrefix, req, "10", "efc", "nas", fsID)
			}
		} else {
			fsID := GetFsIDByCpfsServer(opt.Server)
			if len(fsID) != 0 {
				utils.WriteMetricsInfo(metricsPathPrefix, req, "10", "efc", "cpfs", fsID)
			}
		}

	}
	// change the mode
	if opt.Mode != "" && opt.Path != "/" {
		var args []string
		if opt.ModeType == "recursive" {
			args = append(args, "-R")
		}
		args = append(args, opt.Mode, mountPath)
		cmd := exec.Command("chmod", args...)
		done := make(chan struct{})
		go func() {
			if err := cmd.Run(); err != nil {
				log.Errorf("Nas chmod cmd fail: %s %s", cmd, err)
			} else {
				log.Infof("Nas chmod cmd success: %s", cmd)
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			log.Infof("Chmod use more than 1s, running in Concurrency: %s", mountPath)
		}
	}

	// check mount
	notMounted, err = ns.mounter.IsLikelyNotMountPoint(mountPath)
	if err != nil {
		log.Errorf("check mount point %s: %v", mountPath, err)
		return &csi.NodePublishVolumeResponse{}, status.Errorf(codes.Internal, err.Error())
	}
	if notMounted {
		return nil, errors.New("Check mount fail after mount:" + mountPath)
	}

	saveVolumeData(opt, mountPath)
	log.Infof("NodePublishVolume:: Volume %s Mount success on mountpoint: %s", req.VolumeId, mountPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

// /var/lib/kubelet/pods/5e03c7f7-2946-4ee1-ad77-2efbc4fdb16c/volumes/kubernetes.io~csi/nas-f5308354-725a-4fd3-b613-0f5b384bd00e/mount
func (ns *nodeServer) mountLosetupPv(mountPoint string, opt *Options, volumeID string) error {
	pathList := strings.Split(mountPoint, "/")
	if len(pathList) != 10 {
		return fmt.Errorf("mountLosetupPv: mountPoint format error, %s", mountPoint)
	}

	podID := pathList[5]
	pvName := pathList[8]

	// /mnt/nasplugin.alibabacloud.com/6c690876-74aa-46f6-a301-da7f4353665d/pv-losetup/
	nfsPath := filepath.Join(NasMntPoint, podID, pvName)
	if err := utils.CreateDest(nfsPath); err != nil {
		return fmt.Errorf("mountLosetupPv: create nfs mountPath error %s ", err.Error())
	}
	//mount nas to use losetup dev
	err := doMount(ns.mounter, opt, nfsPath, volumeID, podID)
	if err != nil {
		return fmt.Errorf("mountLosetupPv: mount losetup volume failed: %s", err.Error())
	}

	lockFile := filepath.Join(nfsPath, LoopLockFile)
	if opt.LoopLock == "true" && ns.isLosetupUsed(lockFile, opt, volumeID) {
		return fmt.Errorf("mountLosetupPv: nfs losetup file is used by others %s", lockFile)
	}
	imgFile := filepath.Join(nfsPath, LoopImgFile)
	_, err = os.Stat(imgFile)
	if err != nil {
		if os.IsNotExist(err) && opt.LoopImageSize != 0 {
			if err := createLosetupPv(nfsPath, int64(opt.LoopImageSize)); err != nil {
				return fmt.Errorf("init loop image file: %w", err)
			}
			log.Infof("created loop image file for %s, size: %d", pvName, opt.LoopImageSize)
		} else {
			return err
		}
	}
	failedFile := filepath.Join(nfsPath, Resize2fsFailedFilename)
	if utils.IsFileExisting(failedFile) {
		// path/to/whatever does not exist
		cmd := exec.Command("fsck", "-a", imgFile)
		// cmd := fmt.Sprintf(Resize2fsFailedFixCmd, NsenterCmd, imgFile)
		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("mountLosetupPv: mount nfs losetup error %s", err.Error())
		}
		err = os.Remove(failedFile)
		if err != nil {
			log.Errorf("mountLosetupPv: failed to remove failed file: %v", err)
		}
	}
	err = ns.mounter.Mount(imgFile, mountPoint, "", []string{"loop"})
	if err != nil {
		return fmt.Errorf("mountLosetupPv: mount nfs losetup error %s", err.Error())
	}
	lockContent := ns.config.NodeName + ":" + ns.config.NodeIP
	if err := os.WriteFile(lockFile, ([]byte)(lockContent), 0644); err != nil {
		return err
	}
	return nil
}

func (ns *nodeServer) isLosetupUsed(lockFile string, opt *Options, volumeID string) bool {
	if !utils.IsFileExisting(lockFile) {
		return false
	}
	fileCotent := utils.GetFileContent(lockFile)
	contentParts := strings.Split(fileCotent, ":")
	if len(contentParts) != 2 || contentParts[0] == "" || contentParts[1] == "" {
		return true
	}

	oldNodeName := contentParts[0]
	oldNodeIP := contentParts[1]
	if ns.config.NodeName == oldNodeName {
		mounted, err := isLosetupMount(volumeID)
		if err != nil {
			log.Errorf("can not determine whether %s losetup image used: %v", volumeID, err)
			return true
		}
		if !mounted {
			log.Warnf("Lockfile(%s) exist, but Losetup image not mounted %s.", lockFile, opt.Path)
			return false
		}
		log.Warnf("Lockfile(%s) exist, but Losetup image mounted %s.", lockFile, opt.Path)
		return true
	}

	stat, err := utils.Ping(oldNodeIP)
	if err != nil {
		log.Warnf("Ping node %s, but get error: %s, consider as volume used", oldNodeIP, err.Error())
		return true
	}
	if stat.PacketLoss == 100 {
		log.Warnf("Cannot connect to node %s, consider the node as shutdown(%s).", oldNodeIP, lockFile)
		return false
	}
	return true
}

func validateNodeUnpublishVolumeRequest(req *csi.NodeUnpublishVolumeRequest) error {
	valid, err := utils.ValidatePath(req.GetTargetPath())
	if !valid {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}
	return nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	log.Infof("NodeUnpublishVolume:: Starting umount nas volume %s with req: %+v", req.VolumeId, req)
	err := validateNodeUnpublishVolumeRequest(req)
	if err != nil {
		return nil, err
	}

	targetPath := req.TargetPath
	if !ns.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for %s", req.VolumeId)
	}
	defer ns.locks.Release(req.VolumeId)

	if err := cleanupMountpoint(ns.mounter, targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount %s: %v", targetPath, err)
	}
	log.Infof("NodeUnpublishVolume: unmount volume on %s successfully", targetPath)

	// when mixruntime mode enabled, try to remove ../alibabacloudcsiplugin.json
	if ns.config.EnableMixRuntime {
		fileName := filepath.Join(targetPath, utils.CsiPluginRunTimeFlagFile)
		err := os.Remove(fileName)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, status.Errorf(codes.Internal, "NodeUnpublishVolume(runv): remove %s: %v", fileName, err)
			}
		} else {
			log.Infof("NodeUnpublishVolume(runv): Remove runv file successful: %s", fileName)
		}
	}

	// when losetup enabled, try to cleanup mountpoint under /mnt/nasplugin.alibabacloud.com/
	if ns.config.EnableLosetup {
		if err := unmountLosetupPv(ns.mounter, targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(
	ctx context.Context,
	req *csi.NodeStageVolumeRequest) (
	*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (ns *nodeServer) NodeUnstageVolume(
	ctx context.Context,
	req *csi.NodeUnstageVolumeRequest) (
	*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (
	*csi.NodeExpandVolumeResponse, error) {
	log.Infof("NodeExpandVolume: nas expand volume with %v", req)
	if ns.config.EnableLosetup {
		if err := ns.LosetupExpandVolume(req); err != nil {
			return nil, fmt.Errorf("NodeExpandVolume: error with %v", err)
		}
	}
	return &csi.NodeExpandVolumeResponse{}, nil
}

// LosetupExpandVolume tag
func (ns *nodeServer) LosetupExpandVolume(req *csi.NodeExpandVolumeRequest) error {
	pathList := strings.Split(req.VolumePath, "/")
	if len(pathList) != 10 {
		log.Warnf("NodeExpandVolume: Mountpoint Format illegal, just skip expand %s", req.VolumePath)
		return nil
	}
	podID := pathList[5]
	pvName := pathList[8]

	// /mnt/nasplugin.alibabacloud.com/6c690876-74aa-46f6-a301-da7f4353665d/pv-losetup/
	nfsPath := filepath.Join(NasMntPoint, podID, pvName)
	imgFile := filepath.Join(nfsPath, LoopImgFile)
	if utils.IsFileExisting(imgFile) {
		volSizeBytes := req.GetCapacityRange().GetRequiredBytes()
		err := losetup.TruncateFile(imgFile, volSizeBytes)
		if err != nil {
			log.Errorf("NodeExpandVolume: nas resize img file error %v", err)
			return fmt.Errorf("NodeExpandVolume: nas resize img file error, %v", err)
		}
		devices, err := losetup.List(losetup.WithAssociatedFile(imgFile))
		if err != nil {
			log.Errorf("NodeExpandVolume: search losetup device error %v", err)
			return fmt.Errorf("NodeExpandVolume: search losetup device error, %v", err)
		}
		if len(devices) == 0 {
			return fmt.Errorf("Not found this losetup device %s", imgFile)
		}
		loopDev := devices[0].Name
		if err := losetup.Resize(loopDev); err != nil {
			log.Errorf("NodeExpandVolume: resize device error %v", err)
			return fmt.Errorf("NodeExpandVolume: resize device file error, %v", err)
		}

		// chkCmd := fmt.Sprintf("%s fsck -a %s", NsenterCmd, imgFile)
		// _, err = utils.Run(chkCmd)
		// if err != nil {
		// 	return fmt.Errorf("Check losetup image error %s", err.Error())
		// }
		if err := exec.Command("resize2fs", loopDev).Run(); err != nil {
			log.Errorf("NodeExpandVolume: resize filesystem error %v", err)
			failedFile := filepath.Join(nfsPath, Resize2fsFailedFilename)
			if !utils.IsFileExisting(failedFile) {
				// path/to/whatever does not exist
				if werr := os.WriteFile(failedFile, ([]byte)(""), 0644); werr != nil {
					return fmt.Errorf("NodeExpandVolume: write file err %s, resizefs err: %s", werr, err)
				}
			}
			return fmt.Errorf("NodeExpandVolume: resize filesystem error, %v", err)
		}
		log.Infof("NodeExpandVolume, losetup volume expand successful %s to %d B", req.VolumeId, volSizeBytes)
	} else {
		log.Infof("NodeExpandVolume, only support losetup nas pv type for volume expand %s", req.VolumeId)
	}
	return nil
}

// NodeGetCapabilities node get capability
func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	// currently there is a single NodeServer capability according to the spec
	nscap := &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
			},
		},
	}
	nscap2 := &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
			},
		},
	}

	// Nas Metric enable config
	nodeSvcCap := []*csi.NodeServiceCapability{nscap2}
	if ns.config.EnableVolumeStats {
		nodeSvcCap = []*csi.NodeServiceCapability{nscap, nscap2}
	}

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: nodeSvcCap,
	}, nil
}

// NodeGetVolumeStats used for csi metrics
func (ns *nodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	targetPath := req.GetVolumePath()
	return utils.GetMetrics(targetPath)
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
