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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdkerrors "github.com/aliyun/alibaba-cloud-sdk-go/sdk/errors"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/losetup"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/version"
	log "github.com/sirupsen/logrus"
	mountutils "k8s.io/mount-utils"
)

const (
	// RegionTag is region id
	RegionTag = "region-id"
	// LoopLockFile lock file for nas loopsetup
	LoopLockFile = "loopsetup.nas.csi.alibabacloud.com.lck"
	// LoopImgFile image file for nas loopsetup
	LoopImgFile = "loopsetup.nas.csi.alibabacloud.com.img"
	// Resize2fsFailedFilename ...
	Resize2fsFailedFilename = "resize2fs_failed.txt"
	// Resize2fsFailedFixCmd ...
	Resize2fsFailedFixCmd = "%s fsck -a %s"
	// GiB bytes
	GiB = 1 << 30
	// see https://help.aliyun.com/zh/nas/modify-the-maximum-number-of-concurrent-nfs-requests
	TcpSlotTableEntries      = "/proc/sys/sunrpc/tcp_slot_table_entries"
	TcpSlotTableEntriesValue = "128\n"
)

var (
	// KubernetesAlicloudIdentity is the system identity for ecs client request
	KubernetesAlicloudIdentity = fmt.Sprintf("Kubernetes.Alicloud/CsiProvision.Nas-%s", version.VERSION)
)

// RoleAuth define STS Token Response
type RoleAuth struct {
	AccessKeyID     string
	AccessKeySecret string
	Expiration      time.Time
	SecurityToken   string
	LastUpdated     time.Time
	Code            string
}

// doMount execute the mount command for nas dir
func doMount(mounter mountutils.Interface, fsType, clientType, nfsProtocol, nfsServer, nfsPath, nfsVers, mountOptions, mountPoint, volumeID, podUID string) error {
	source := fmt.Sprintf("%s:%s", nfsServer, nfsPath)
	var combinedOptions []string
	if mountOptions != "" {
		combinedOptions = append(combinedOptions, mountOptions)
	}
	var (
		mountFstype    string
		isPathNotFound func(error) bool
	)
	switch clientType {
	case EFCClient:
		combinedOptions = append(combinedOptions, "efc", fmt.Sprintf("bindtag=%s", volumeID), fmt.Sprintf("client_owner=%s", podUID))
		switch fsType {
		case "standard":
		case "cpfs":
			combinedOptions = append(combinedOptions, "protocol=nfs3")
		default:
			return errors.New("EFC Client don't support this storage type:" + fsType)
		}
		mountFstype = "alinas"
		// err = mounter.Mount(source, mountPoint, "alinas", combinedOptions)
		isPathNotFound = func(err error) bool {
			return strings.Contains(err.Error(), "Failed to bind mount")
		}
	case NativeClient:
		switch fsType {
		case "cpfs":
		default:
			return errors.New("Native Client don't support this storage type:" + fsType)
		}
		mountFstype = "cpfs"
	default:
		//NFS Mount(Capacdity/Performance Extreme Nas、Cpfs2.0, AliNas)
		versStr := fmt.Sprintf("vers=%s", nfsVers)
		if !strings.Contains(mountOptions, versStr) {
			combinedOptions = append(combinedOptions, versStr)
		}
		mountFstype = nfsProtocol
		isPathNotFound = func(err error) bool {
			return strings.Contains(err.Error(), "reason given by server: No such file or directory") || strings.Contains(err.Error(), "access denied by server while mounting")
		}
	}
	err := mounter.Mount(source, mountPoint, mountFstype, combinedOptions)
	if err == nil {
		return nil
	}
	if isPathNotFound == nil || !isPathNotFound(err) {
		return err
	}

	rootPath := "/"
	if fsType == "cpfs" || mountFstype == MountProtocolCPFSNFS || strings.Contains(nfsServer, "extreme.nas.aliyuncs.com") {
		rootPath = "/share"
	}
	relPath, relErr := filepath.Rel(rootPath, nfsPath)
	if relErr != nil || relPath == "." {
		return err
	}
	rootSource := fmt.Sprintf("%s:%s", nfsServer, rootPath)
	log.Infof("trying to create subpath %s", rootSource)
	_ = os.MkdirAll(NasTempMntPath, os.ModePerm)
	tmpPath, err := os.MkdirTemp(NasTempMntPath, volumeID+"_")
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath)
	if err := mounter.Mount(rootSource, tmpPath, mountFstype, combinedOptions); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(tmpPath, relPath), os.ModePerm); err != nil {
		return err
	}
	if err := cleanupMountpoint(mounter, tmpPath); err != nil {
		log.Errorf("failed to cleanup tmp mountpoint %s: %v", tmpPath, err)
	}
	return mounter.Mount(source, mountPoint, mountFstype, combinedOptions)
}

// check system config,
// if tcp_slot_table_entries not set to 128, just config.
func checkSystemNasConfig() error {
	data, err := os.ReadFile(TcpSlotTableEntries)
	if err == nil && string(data) == TcpSlotTableEntriesValue {
		return nil
	}
	return os.WriteFile(TcpSlotTableEntries, []byte(TcpSlotTableEntriesValue), os.ModePerm)
}

// ParseMountFlags parse mountOptions
func ParseMountFlags(mntOptions []string) (string, string) {
	var vers string
	var otherOptions []string
	for _, options := range mntOptions {
		for _, option := range mounter.SplitMountOptions(options) {
			if option == "" {
				continue
			}
			key, value, found := strings.Cut(option, "=")
			if found && key == "vers" {
				vers = value
			} else {
				otherOptions = append(otherOptions, option)
			}
		}
	}
	if vers == "3.0" {
		vers = "3"
	}
	return vers, strings.Join(otherOptions, ",")
}

func createLosetupPv(fullPath string, volSizeBytes int64) error {
	fileName := filepath.Join(fullPath, LoopImgFile)
	if utils.IsFileExisting(fileName) {
		log.Infof("createLosetupPv: image file is exist, just skip: %s", fileName)
		return nil
	}
	if err := losetup.TruncateFile(fileName, volSizeBytes); err != nil {
		return err
	}
	return exec.Command("mkfs.ext4", "-F", "-m0", fileName).Run()
}

// /var/lib/kubelet/pods/5e03c7f7-2946-4ee1-ad77-2efbc4fdb16c/volumes/kubernetes.io~csi/nas-f5308354-725a-4fd3-b613-0f5b384bd00e/mount
func mountLosetupPv(mounter mountutils.Interface, mountPoint string, opt *Options, volumeID string) error {
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
	err := doMount(mounter, "nas", "nfsclient", opt.MountProtocol, opt.Server, opt.Path, opt.Vers, opt.Options, nfsPath, volumeID, podID)
	if err != nil {
		return fmt.Errorf("mountLosetupPv: mount losetup volume failed: %s", err.Error())
	}

	lockFile := filepath.Join(nfsPath, LoopLockFile)
	if opt.LoopLock == "true" && isLosetupUsed(mounter, lockFile, opt, volumeID) {
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
	err = mounter.Mount(imgFile, mountPoint, "", []string{"loop"})
	if err != nil {
		return fmt.Errorf("mountLosetupPv: mount nfs losetup error %s", err.Error())
	}
	lockContent := GlobalConfigVar.NodeID + ":" + GlobalConfigVar.NodeIP
	if err := os.WriteFile(lockFile, ([]byte)(lockContent), 0644); err != nil {
		return err
	}
	return nil
}

func isLosetupUsed(mounter mountutils.Interface, lockFile string, opt *Options, volumeID string) bool {
	if !utils.IsFileExisting(lockFile) {
		return false
	}
	fileCotent := utils.GetFileContent(lockFile)
	contentParts := strings.Split(fileCotent, ":")
	if len(contentParts) != 2 || contentParts[0] == "" || contentParts[1] == "" {
		return true
	}

	oldNodeID := contentParts[0]
	oldNodeIP := contentParts[1]
	if GlobalConfigVar.NodeID == oldNodeID {
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

func unmountLosetupPv(mounter mountutils.Interface, mountPoint string) error {
	pathList := strings.Split(mountPoint, "/")
	if len(pathList) != 10 {
		log.Infof("MountPoint not format as losetup type: %s", mountPoint)
		return nil
	}
	podID := pathList[5]
	pvName := pathList[8]
	nfsPath := filepath.Join(NasMntPoint, podID, pvName)
	imgFile := filepath.Join(nfsPath, LoopImgFile)
	lockFile := filepath.Join(nfsPath, LoopLockFile)
	if utils.IsFileExisting(imgFile) {
		log.Infof("cleanup the losetup mountpoint due to the existence of image file %s", imgFile)
		if utils.IsFileExisting(lockFile) {
			if err := os.Remove(lockFile); err != nil {
				return fmt.Errorf("checkLosetupUnmount: remove lock file error %v", err)
			}
		}
		if err := cleanupMountpoint(mounter, nfsPath); err != nil {
			return fmt.Errorf("checkLosetupUnmount: umount nfs path error %v", err)
		}
		log.Infof("Losetup Unmount successful %s", mountPoint)
	}
	return nil
}

func isLosetupMount(volumeID string) (bool, error) {
	devices, err := losetup.List()
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		if strings.Contains(device.BackFile, volumeID+"/"+LoopImgFile) {
			return true, nil
		}
	}
	return false, nil
}

func isValidCnfsParameter(server string, cnfsName string) error {
	if len(server) == 0 && len(cnfsName) == 0 {
		msg := "Server and Cnfs need to be configured at least one."
		log.Errorf(msg)
		return errors.New(msg)
	}

	if len(server) != 0 && len(cnfsName) != 0 {
		msg := "Server and Cnfs can only be configured to use one."
		log.Errorf(msg)
		return errors.New(msg)
	}
	return nil
}

// GetFsIDByNasServer func is get fsID from serverName
func GetFsIDByNasServer(server string) string {
	if len(server) == 0 {
		return ""
	}
	serverArray := strings.Split(server, "-")
	return serverArray[0]
}

// GetFsIDByCpfsServer func is get fsID from serverName
func GetFsIDByCpfsServer(server string) string {
	if len(server) == 0 {
		return ""
	}
	serverArray := strings.Split(server, "-")
	return serverArray[0] + "-" + serverArray[1]
}

func saveVolumeData(opt *Options, mountPath string) {
	// save volume data to json file
	if utils.IsKataInstall() {
		volumeData := map[string]string{}
		volumeData["csi.alibabacloud.com/version"] = opt.Vers
		if len(opt.Options) != 0 {
			volumeData["csi.alibabacloud.com/options"] = opt.Options
		}
		if len(opt.Server) != 0 {
			volumeData["csi.alibabacloud.com/server"] = opt.Server
		}
		if len(opt.Path) != 0 {
			volumeData["csi.alibabacloud.com/path"] = opt.Path
		}
		fileName := filepath.Join(filepath.Dir(mountPath), utils.VolDataFileName)
		if strings.HasSuffix(mountPath, "/") {
			fileName = filepath.Join(filepath.Dir(filepath.Dir(mountPath)), utils.VolDataFileName)
		}
		if err := utils.AppendJSONData(fileName, volumeData); err != nil {
			log.Warnf("NodePublishVolume: append nas volume spec to %s with error: %s", fileName, err.Error())
		}
	}
}

func cleanupMountpoint(mounter mountutils.Interface, mountPath string) (err error) {
	forceUnmounter, ok := mounter.(mountutils.MounterForceUnmounter)
	if ok {
		err = mountutils.CleanupMountWithForce(mountPath, forceUnmounter, false, time.Second*30)
	} else {
		err = mountutils.CleanupMountPoint(mountPath, mounter, false)
	}
	return
}

func isMountTargetNotFoundError(err error) bool {
	var serverErr *sdkerrors.ServerError
	if !errors.As(err, &serverErr) || serverErr == nil {
		return false
	}
	code := serverErr.ErrorCode()
	return code == "InvalidParam.MountTargetDomain" || code == "InvalidMountTarget.NotFound"
}
