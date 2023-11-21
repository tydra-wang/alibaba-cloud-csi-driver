package nas

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/alibabacloud-go/nas-20170626/v3/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/container-storage-interface/spec/lib/go/csi"
	csicommon "github.com/kubernetes-csi/drivers/pkg/csi-common"
	cnfsv1beta1 "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cnfs/v1beta1"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/nas/internal"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	subpathArchiveFinalizer  = "csi.alibabacloud.com/nas-subpath-archive"
	subpathDeletionFinalizer = "csi.alibabacloud.com/nas-subpath-deletion"
)

type subpathControllerServer struct {
	*csicommon.DefaultControllerServer
	skipCreatingSubpath     bool
	enableDeletionFinalizer bool
	crdClient               dynamic.Interface
	kubeClient              kubernetes.Interface
	nasClient               *internal.NasClientV2
}

func (cs *subpathControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// parse parameters
	parameters := req.Parameters
	var (
		path           string
		filesystemId   string
		filesystemType string
	)
	volumeContext := map[string]string{}
	// using cnfs or not
	if cnfsName := parameters["containerNetworkFileSystem"]; cnfsName != "" {
		cnfs, err := cnfsv1beta1.GetCnfsObject(cs.crdClient, cnfsName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, status.Errorf(codes.InvalidArgument, "CNFS not found: %s", cnfsName)
			}
			return nil, status.Errorf(codes.Internal, "failed to get CNFS %s: %v", cnfsName, err)
		}
		if path = parameters["path"]; path == "" {
			path = "/"
		} else {
			path = filepath.Clean(path)
		}
		filesystemId = cnfs.Status.FsAttributes.FilesystemID
		if filesystemId == "" {
			return nil, status.Error(codes.InvalidArgument, "missing server or filesystemId in CNFS status")
		}
		filesystemType = cnfs.Status.FsAttributes.FilesystemType
		// set volumeContext
		volumeContext["containerNetworkFileSystem"] = cnfs.Name
	} else {
		var server string
		server, path = muxServerSelector.SelectNfsServer(parameters["server"])
		filesystemId, _, _ := strings.Cut(server, "-")
		if server == "" || filesystemId == "" {
			return nil, status.Error(codes.InvalidArgument, "invalid nas server")
		}
		var err error
		filesystemType, err = cs.getFilesystemType(filesystemId)
		if err != nil {
			return nil, err
		}
		// set volumeContext
		if protocol := parameters["mountProtocol"]; protocol != "" {
			volumeContext["mountProtocol"] = protocol
		}
		volumeContext["server"] = server
	}
	// fill volumeContext
	path = filepath.Join(path, req.Name)
	volumeContext["volumeAs"] = "subpath"
	volumeContext["path"] = path
	if mountType := parameters["mountType"]; mountType != "" {
		volumeContext["mountType"] = mountType
	}
	if mode := parameters["mode"]; mode != "" {
		volumeContext["mode"] = mode
		modeType := parameters["modeType"]
		if modeType == "" {
			modeType = "non-recursive"
		}
		volumeContext["modeType"] = modeType
	}
	if options := parameters["options"]; options != "" {
		volumeContext["options"] = options
	}

	capacity := req.GetCapacityRange().GetRequiredBytes()
	resp := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      req.Name,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
	// Only standard filesystems support "CreateDir" and "SetDirQuota" APIs.
	// Subpaths of other types filesystems will be truly created when NodePublishVolume.
	if filesystemType != internal.FilesystemTypeStandard {
		return resp, nil
	}
	if cs.skipCreatingSubpath {
		logrus.Infof("skip creating subpath directory for %s", req.Name)
		return resp, nil
	}
	logrus.WithFields(logrus.Fields{
		"filesystemId": filesystemId,
		"path":         path,
	}).Info("start to create subpath directory for volume")
	// create dir
	if err := cs.nasClient.CreateDir(&sdk.CreateDirRequest{
		FileSystemId:  &filesystemId,
		OwnerGroupId:  tea.Int32(0),
		OwnerUserId:   tea.Int32(0),
		Permission:    tea.String("0777"),
		RootDirectory: &path,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "nas:CreateDir failed: %v", err)
	}
	// set dir quota
	if parameters["mountType"] != "losetup" && (parameters["volumeCapacity"] == "true" || parameters["allowVolumeExpansion"] == "true") {
		quota := (capacity + GiB - 1) >> 30
		resp.Volume.CapacityBytes = quota << 30
		volumeContext["volumeCapacity"] = "true"
		if err := cs.nasClient.SetDirQuota(&sdk.SetDirQuotaRequest{
			FileSystemId: &filesystemId,
			Path:         &path,
			SizeLimit:    &quota,
			QuotaType:    tea.String("Enforcement"),
			UserType:     tea.String("AllUsers"),
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "nas:SetDirQuota failed: %v", err)
		}
	}

	return resp, nil
}

func (cs *subpathControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	pv := ctx.Value("PersistentVolume").(*corev1.PersistentVolume)
	attributes := pv.Spec.CSI.VolumeAttributes
	var (
		filesystemId      string
		path              = attributes["path"]
		recycleBinEnabled bool
	)
	// using cnfs or not
	if cnfsName := attributes["containerNetworkFileSystem"]; cnfsName != "" {
		cnfs, err := cnfsv1beta1.GetCnfsObject(cs.crdClient, cnfsName)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get CNFS %s: %v", cnfsName, err)
		}
		filesystemId = cnfs.Status.FsAttributes.FilesystemID
		recycleBinEnabled, _ = strconv.ParseBool(cnfs.Status.FsAttributes.EnableTrashCan)
	} else {
		server := attributes["server"]
		filesystemId, _, _ = strings.Cut(server, "-")
		var err error
		recycleBinEnabled, err = cs.isRecycleBinEnabled(filesystemId)
		if err != nil {
			return nil, err
		}
	}
	if filesystemId == "" {
		return nil, status.Error(codes.InvalidArgument, "empty filesystemId")
	}
	// cancel dir quota
	if attributes["volumeCapacity"] == "true" {
		if err := cs.nasClient.CancelDirQuota(&sdk.CancelDirQuotaRequest{
			FileSystemId: &filesystemId,
			Path:         &path,
			UserType:     tea.String("AllUsers"),
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "nas:CancelDirQuota failed: %v", err)
		}
	}
	if !cs.enableDeletionFinalizer {
		logrus.Warn("deletion finalizer not enabled, skip subpath deletion")
		return &csi.DeleteVolumeResponse{}, nil
	}
	// Patch finalizer on PV if need delete or archive subpath. The true deletion/archiving will be executed
	// by storage-controller who is responsible to remove the finalzier.
	// Delete subpath only when parameters["archiveOnDelete"] exists and has a false value.
	sc, err := cs.kubeClient.StorageV1().StorageClasses().Get(ctx, pv.Spec.StorageClassName, metav1.GetOptions{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}
	archive := true
	if value, exists := sc.Parameters["archiveOnDelete"]; exists {
		boolValue, err := strconv.ParseBool(value)
		if err == nil {
			archive = boolValue
		}
	}
	var finalizer string
	if archive {
		finalizer = subpathArchiveFinalizer
	} else {
		if !recycleBinEnabled {
			logrus.WithFields(logrus.Fields{
				"filesystemId": filesystemId,
				"pv":           pv.Name,
			}).Warnf("skip subpath deletion because recycle bin of filesystem not enabled")
			return &csi.DeleteVolumeResponse{}, nil
		}
		finalizer = subpathDeletionFinalizer
	}
	if err := cs.patchFinalizerOnPV(ctx, pv, finalizer); err != nil {
		return nil, err
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *subpathControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	pv := ctx.Value("PersistentVolume").(*corev1.PersistentVolume)
	attributes := pv.Spec.CSI.VolumeAttributes
	var (
		filesystemId string
		path         = attributes["path"]
	)
	// using cnfs or not
	if cnfsName := attributes["containerNetworkFileSystem"]; cnfsName != "" {
		cnfs, err := cnfsv1beta1.GetCnfsObject(cs.crdClient, cnfsName)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get CNFS %s: %v", cnfsName, err)
		}
		filesystemId = cnfs.Status.FsAttributes.FilesystemID
	} else {
		server := attributes["server"]
		filesystemId, _, _ = strings.Cut(server, "-")
	}
	if filesystemId == "" {
		return nil, status.Error(codes.InvalidArgument, "empty filesystemId")
	}
	capacity := req.GetCapacityRange().GetRequiredBytes()
	if attributes["volumeCapacity"] == "true" {
		quota := (capacity + GiB - 1) >> 30
		if err := cs.nasClient.SetDirQuota(&sdk.SetDirQuotaRequest{
			FileSystemId: &filesystemId,
			Path:         &path,
			SizeLimit:    &quota,
			QuotaType:    tea.String("Enforcement"),
			UserType:     tea.String("AllUsers"),
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "nas:SetDirQuota failed: %v", err)
		}
		return &csi.ControllerExpandVolumeResponse{CapacityBytes: quota << 30}, nil
	}
	if attributes["mountType"] == "losetup" {
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         capacity,
			NodeExpansionRequired: true,
		}, nil
	}
	logrus.Warn("volume capacity not enabled when provision, skip quota expandsion")
	return &csi.ControllerExpandVolumeResponse{CapacityBytes: capacity}, nil
}

func (cs *subpathControllerServer) patchFinalizerOnPV(ctx context.Context, pv *corev1.PersistentVolume, finalizer string) error {
	for _, f := range pv.Finalizers {
		if f == finalizer {
			return nil
		}
	}
	patch := corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Finalizers: []string{finalizer},
		},
	}
	patchData, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = cs.kubeClient.CoreV1().PersistentVolumes().Patch(ctx, pv.Name, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	logrus.Infof("patched finalizer %s on pv %s", finalizer, pv.Name)
	return nil
}

func (cs *subpathControllerServer) getFilesystemType(filesystemId string) (string, error) {
	resp, err := cs.nasClient.DescribeFileSystemBriefInfo(filesystemId)
	if err != nil {
		return "", status.Errorf(codes.Internal, "nas:DescribeFileSystemBriefInfos failed: %v", err)
	}
	if resp.Body == nil || resp.Body.FileSystems == nil || len(resp.Body.FileSystems.FileSystem) == 0 {
		return "", status.Errorf(codes.InvalidArgument, "filesystemId %q not found", filesystemId)
	}
	filesystemInfo := resp.Body.FileSystems.FileSystem[0]
	filesystemType := tea.StringValue(filesystemInfo.FileSystemType)
	logrus.Infof("nas:DescribeFileSystemBriefInfos succeeded, filesystemType of %s is %q", filesystemId, filesystemType)
	return filesystemType, nil
}

func (cs *subpathControllerServer) isRecycleBinEnabled(filesystemId string) (bool, error) {
	resp, err := cs.nasClient.GetRecycleBinAttribute(filesystemId)
	if err != nil {
		return false, status.Errorf(codes.Internal, "nas:GetRecycleBinAttribute failed: %v", err)
	}
	return resp.Body != nil && resp.Body.RecycleBinAttribute != nil && tea.StringValue(resp.Body.RecycleBinAttribute.Status) == "Enable", nil
}

var muxServerSelector = &rrMuxServerSelector{
	servers: make(map[string]int),
}

type rrMuxServerSelector struct {
	sync.Mutex
	servers map[string]int
}

func (s *rrMuxServerSelector) SelectNfsServer(muxServer string) (string, string) {
	s.Lock()
	defer s.Unlock()
	var servers, paths []string
	for _, str := range strings.Split(muxServer, ",") {
		server, path := s.parse(str)
		if server == "" {
			return "", ""
		}
		servers = append(servers, server)
		paths = append(paths, path)
	}
	if len(servers) < 2 {
		return servers[0], paths[0]
	}
	index := 0
	if i, ok := s.servers[muxServer]; ok {
		index = (i + 1) % len(servers)
	}
	s.servers[muxServer] = index
	return servers[index], paths[index]
}

func (s *rrMuxServerSelector) parse(serverWithPath string) (server, path string) {
	server, path, found := strings.Cut(serverWithPath, ":")
	if found && path != "" {
		path = filepath.Clean(path)
	} else {
		path = "/"
	}
	return
}