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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	csicommon "github.com/kubernetes-csi/drivers/pkg/csi-common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type controllerServer struct {
	*csicommon.DefaultControllerServer
	locks      *utils.VolumeLocks
	kubeClient kubernetes.Interface
	// controller server modes
	filesystemServer, subpathServer, sharepathServer csi.ControllerServer
}

// NewControllerServer is to create controller server
func NewControllerServer(d *csicommon.CSIDriver) (csi.ControllerServer, error) {
	defaultServer := csicommon.NewDefaultControllerServer(d)
	subpathServer, err := newSubpathControllerServer(defaultServer)
	if err != nil {
		return nil, err
	}
	sharepathServer := newSharepathControllerServer(defaultServer)
	filesystemServer := newFilesystemControllerServer(defaultServer)
	c := &controllerServer{
		DefaultControllerServer: defaultServer,
		locks:                   utils.NewVolumeLocks(),
		kubeClient:              GlobalConfigVar.KubeClient,
		filesystemServer:        filesystemServer,
		subpathServer:           subpathServer,
		sharepathServer:         sharepathServer,
	}
	return c, nil
}

func validateCreateVolumeRequest(req *csi.CreateVolumeRequest) error {
	log.Infof("Starting nfs validate create volume request %s, %v", req.Name, req)
	valid, err := utils.CheckRequestArgs(req.GetParameters())
	if !valid {
		return status.Errorf(codes.InvalidArgument, err.Error())
	}
	return nil
}

func (cs *controllerServer) volumeAs(volumeAs string) (csi.ControllerServer, error) {
	switch volumeAs {
	case "", "subpath":
		return cs.subpathServer, nil
	case "sharepath":
		return cs.sharepathServer, nil
	case "filesystem":
		return cs.filesystemServer, nil
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid volumeAs: %q", volumeAs)
	}
}

func (cs *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	log.Infof("start provision for %s", req.Name)
	if err := validateCreateVolumeRequest(req); err != nil {
		return nil, err
	}
	if !cs.locks.TryAcquire(req.Name) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for volume %s", req.Name)
	}
	defer cs.locks.Release(req.Name)

	server, err := cs.volumeAs(req.Parameters["volumeAs"])
	if err != nil {
		return nil, err
	}
	resp, err := server.CreateVolume(ctx, req)
	if err == nil {
		log.WithFields(log.Fields{"response": resp, "request": req}).Info("provision succeeded")
	}
	return resp, err
}

func (cs *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	log.Infof("DeleteVolume: Starting deleting volume %s", req.GetVolumeId())
	if !cs.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for volume %s", req.VolumeId)
	}
	defer cs.locks.Release(req.VolumeId)

	pv, err := cs.kubeClient.CoreV1().PersistentVolumes().Get(ctx, req.VolumeId, metav1.GetOptions{})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	server, err := cs.volumeAs(pv.Spec.CSI.VolumeAttributes["volumeAs"])
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, "PersistentVolume", pv)
	resp, err := server.DeleteVolume(ctx, req)
	if err == nil {
		log.WithFields(log.Fields{"response": resp, "request": req}).Info("succeeded to delete volume")
	}
	return resp, err
}

func (cs *controllerServer) ControllerExpandVolume(
	ctx context.Context,
	req *csi.ControllerExpandVolumeRequest,
) (*csi.ControllerExpandVolumeResponse, error) {
	log.Infof("ControllerExpandVolume: starting to expand nas volume with %v", req)
	if !cs.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for volume %s", req.VolumeId)
	}
	defer cs.locks.Release(req.VolumeId)

	pv, err := cs.kubeClient.CoreV1().PersistentVolumes().Get(ctx, req.VolumeId, metav1.GetOptions{})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	server, err := cs.volumeAs(pv.Spec.CSI.VolumeAttributes["volumeAs"])
	if err != nil {
		return nil, err
	}

	cs.nasClient = updateNasClient(cs.nasClient, regionID)
	if volumeAs == "filesystem" {
		if deleteVolume == "true" {
			log.Infof("DeleteVolume: Start delete mountTarget %s for volume %s", nfsServer, req.VolumeId)
			if fileSystemID == "" {
				return nil, fmt.Errorf("DeleteVolume: Volume: %s in filesystem mode, with filesystemId empty", req.VolumeId)
			}

			isMountTargetDelete := false
			describeMountTargetRequest := aliNas.CreateDescribeMountTargetsRequest()
			describeMountTargetRequest.FileSystemId = fileSystemID
			describeMountTargetRequest.MountTargetDomain = nfsServer
			_, err := cs.nasClient.DescribeMountTargets(describeMountTargetRequest)
			if err != nil {
				if isMountTargetNotFoundError(err) {
					log.Infof("DeleteVolume: Volume %s MountTarget %s already delete", req.VolumeId, nfsServer)
					isMountTargetDelete = true
				} else {
					log.Errorf("DescribeMountTargets failed: %v", err)
				}
			}
			if !isMountTargetDelete {
				deleteMountTargetRequest := aliNas.CreateDeleteMountTargetRequest()
				deleteMountTargetRequest.FileSystemId = fileSystemID
				deleteMountTargetRequest.MountTargetDomain = nfsServer
				deleteMountTargetResponse, err := cs.nasClient.DeleteMountTarget(deleteMountTargetRequest)
				if err != nil {
					log.Errorf("DeleteVolume: requestId[%s], volume[%s], fail to delete nas mountTarget %s: with %v", deleteMountTargetResponse.RequestId, req.VolumeId, nfsServer, err)
					errMsg := utils.FindSuggestionByErrorMessage(err.Error(), utils.NasMountTargetDelete)
					return nil, status.Error(codes.Internal, errMsg)
				}
			}
			// remove the pvc mountTarget mapping if exist
			if _, ok := pvcMountTargetMap[req.VolumeId]; ok {
				delete(pvcMountTargetMap, req.VolumeId)
			}
			log.Infof("DeleteVolume: Volume %s MountTarget %s deleted successfully and Start delete filesystem %s", req.VolumeId, nfsServer, fileSystemID)

			deleteFileSystemRequest := aliNas.CreateDeleteFileSystemRequest()
			deleteFileSystemRequest.FileSystemId = fileSystemID
			deleteFileSystemResponse, err := cs.nasClient.DeleteFileSystem(deleteFileSystemRequest)
			if err != nil {
				log.Errorf("DeleteVolume: requestId[%s], volume %s fail to delete nas filesystem %s: with %v", deleteFileSystemResponse.RequestId, req.VolumeId, fileSystemID, err)
				errMsg := utils.FindSuggestionByErrorMessage(err.Error(), utils.NasFilesystemDelete)
				return nil, status.Error(codes.Internal, errMsg)
			}
			// remove the pvc filesystem mapping if exist
			if _, ok := pvcFileSystemIDMap[req.VolumeId]; ok {
				delete(pvcFileSystemIDMap, req.VolumeId)
			}
			log.Infof("DeleteVolume: Volume %s Filesystem %s deleted successfully", req.VolumeId, fileSystemID)
		} else {
			log.Infof("DeleteVolume: Nas Volume %s Filesystem's deleteVolume is [false], skip delete mountTarget and fileSystem", req.VolumeId)
		}

	} else if volumeAs == "subpath" {
		nfsVersion := "3"
		if strings.Contains(nfsOptions, "vers=4.0") {
			nfsVersion = "4.0"
		} else if strings.Contains(nfsOptions, "vers=4.1") {
			nfsVersion = "4.1"
		}

		// parse nfs mount point;
		// pvPath: the path value get from PV spec.
		// nfsPath: the configured nfs path in storageclass in subPath mode.
		tmpPath := pvPath
		if pvPath == "/" || pvPath == "" {
			log.Errorf("DeleteVolume: pvPath cannot be / or empty in subpath mode")
			return nil, status.Error(codes.Internal, "pvPath cannot be / or empty in subpath mode")
		}
		if strings.HasSuffix(pvPath, "/") {
			tmpPath = pvPath[0 : len(pvPath)-1]
		}
		pos := strings.LastIndex(tmpPath, "/")
		nfsPath = pvPath[0:pos]
		if nfsPath == "" {
			nfsPath = "/"
		}

		// set the local mountpoint
		mountPoint := filepath.Join(MntRootPath, req.VolumeId+"-delete")
		// create subpath-delete directory
		if err := doMount(cs.mounter, opt.FSType, opt.ClientType, opt.MountProtocol, nfsServer, nfsPath, nfsVersion, nfsOptions, mountPoint, req.VolumeId, req.VolumeId); err != nil {
			log.Errorf("DeleteVolume: %s, Mount server: %s, nfsPath: %s, nfsVersion: %s, nfsOptions: %s, mountPoint: %s, with error: %s", req.VolumeId, nfsServer, nfsPath, nfsVersion, nfsOptions, mountPoint, err.Error())
			return nil, fmt.Errorf("DeleteVolume: %s, Mount server: %s, nfsPath: %s, nfsVersion: %s, nfsOptions: %s, mountPoint: %s, with error: %s", req.VolumeId, nfsServer, nfsPath, nfsVersion, nfsOptions, mountPoint, err.Error())
		}
		notMounted, err := cs.mounter.IsLikelyNotMountPoint(mountPoint)
		if err != nil {
			log.Errorf("check mount point %s: %v", mountPoint, err)
			return nil, status.Errorf(codes.Internal, err.Error())
		}
		if notMounted {
			return nil, errors.New("Check Mount nfsserver fail " + nfsServer + " error with: ")
		}
		defer cs.mounter.Unmount(mountPoint)

		// pvName is same with volumeId
		pvName := filepath.Base(pvPath)
		deletePath := filepath.Join(mountPoint, pvName)
		if _, err := os.Stat(deletePath); os.IsNotExist(err) {
			log.Infof("Delete: Volume %s, Path %s does not exist, deletion skipped", req.VolumeId, deletePath)
			if _, ok := pvcProcessSuccess[req.VolumeId]; ok {
				delete(pvcProcessSuccess, req.VolumeId)
			}
			return &csi.DeleteVolumeResponse{}, nil
		}

		// only capacity and hibrid nas support quota
		if strings.Contains(nfsServer, ".nas.aliyuncs.com") &&
			!strings.Contains(nfsServer, ".extreme.nas.aliyuncs.com") &&
			!strings.Contains(nfsServer, ".cpfs.nas.aliyuncs.com") {
			fileSystemID = GetFsIDByNasServer(nfsServer)
			if len(fileSystemID) != 0 {
				//1、Does describe dir quota exist?
				//2、If the dir quota exists, cancel the quota before deleting the subdirectory.
				describeDirQuotasReq := aliNas.CreateDescribeDirQuotasRequest()
				describeDirQuotasReq.FileSystemId = fileSystemID
				describeDirQuotasReq.Path = pvPath
				describeDirQuotasRep, err := cs.nasClient.DescribeDirQuotas(describeDirQuotasReq)
				if err != nil {
					log.Errorf("Describe dir quotas is failed, req:%+v, rep:%+v, path:%s, err:%s", describeDirQuotasReq, describeDirQuotasRep, deletePath, err.Error())
				} else {
					isSetQuota := false
					if describeDirQuotasRep != nil && len(describeDirQuotasRep.DirQuotaInfos) != 0 {
						for _, dirQuotaInfo := range describeDirQuotasRep.DirQuotaInfos {
							if dirQuotaInfo.Path == pvPath {
								isSetQuota = true
							}
						}
					}
					if isSetQuota {
						cancelDirQuotaReq := aliNas.CreateCancelDirQuotaRequest()
						cancelDirQuotaReq.FileSystemId = fileSystemID
						cancelDirQuotaReq.Path = pvPath
						cancelDirQuotaReq.UserType = "AllUsers"
						cancelDirQuotaRep, err := cs.nasClient.CancelDirQuota(cancelDirQuotaReq)
						if err != nil {
							log.Errorf("Cancel dir quota is failed, req:%+v, rep:%+v, path:%s, err:%s", cancelDirQuotaReq, cancelDirQuotaRep, deletePath, err.Error())
						}
						if cancelDirQuotaRep != nil && cancelDirQuotaRep.Success {
							log.Infof("Delete Successful: Volume %s fileSystemID %s, cancel dir quota path %s", req.VolumeId, fileSystemID, pvPath)
						} else {
							log.Warnf("Delete Failed: Volume %s, cancel dir quota path %s, req:%+v, rep:%+v", req.VolumeId, pvPath, cancelDirQuotaReq, cancelDirQuotaRep)
						}
					}
				}
			} else {
				log.Errorf("Delete quota is failed: fileSystemID is empty, server:%s", nfsServer)
			}
		}

		// Determine if the "archiveOnDelete" parameter exists.
		// If it exists and has a false value, delete the directory.
		// Otherwise, archive it.
		archive := true
		if archiveOnDelete, exists := storageclass.Parameters["archiveOnDelete"]; exists {
			archiveOnDeleteBool, err := strconv.ParseBool(archiveOnDelete)
			if err == nil {
				archive = archiveOnDeleteBool
			}
		}
		if archive {
			archivePath := filepath.Join(mountPoint, "archived-"+pvName+time.Now().Format(".2006-01-02-15:04:05"))
			if err := os.Rename(deletePath, archivePath); err != nil {
				log.Errorf("Delete Failed: Volume %s, archiving path %s to %s with error: %s", req.VolumeId, deletePath, archivePath, err.Error())
				return nil, errors.New("Check Mount nfsserver fail " + nfsServer + " error with: ")
			}
			log.Infof("Delete Successful: Volume %s, Archiving path %s to %s", req.VolumeId, deletePath, archivePath)
		} else {
			if err := os.RemoveAll(deletePath); err != nil {
				return nil, errors.New("Check Mount nfsserver fail " + nfsServer + " error with: " + err.Error())
			}
			log.Infof("Delete Successful: Volume %s, Removed path %s", req.VolumeId, deletePath)
		}
	} else if volumeAs == "sharepath" {
		log.Infof("Using sharepath mode, the path %s does not need to be deleted.", nfsPath)
	}
	return resp, err
}

func (cs *controllerServer) ValidateVolumeCapabilities(
	ctx context.Context,
	req *csi.ValidateVolumeCapabilitiesRequest,
) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	for _, cap := range req.VolumeCapabilities {
		if cap.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: ""}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.VolumeCapabilities,
		},
	}, nil
}

func (cs *controllerServer) ControllerUnpublishVolume(
	ctx context.Context,
	req *csi.ControllerUnpublishVolumeRequest,
) (*csi.ControllerUnpublishVolumeResponse, error) {
	log.Infof("ControllerUnpublishVolume is called, do nothing by now")
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (cs *controllerServer) ControllerPublishVolume(
	ctx context.Context,
	req *csi.ControllerPublishVolumeRequest,
) (*csi.ControllerPublishVolumeResponse, error) {
	log.Infof("ControllerPublishVolume is called, do nothing by now")
	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (cs *controllerServer) CreateSnapshot(
	ctx context.Context,
	req *csi.CreateSnapshotRequest,
) (*csi.CreateSnapshotResponse, error) {
	log.Infof("CreateSnapshot is called, do nothing now")
	return &csi.CreateSnapshotResponse{}, nil
}

func (cs *controllerServer) DeleteSnapshot(
	ctx context.Context,
	req *csi.DeleteSnapshotRequest,
) (*csi.DeleteSnapshotResponse, error) {
	log.Infof("DeleteSnapshot is called, do nothing now")
	return &csi.DeleteSnapshotResponse{}, nil
}
