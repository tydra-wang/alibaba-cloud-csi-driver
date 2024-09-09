package alinas

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

const (
	fstypeCpfsNfs = "cpfs-nfs"
	fstypeAlinas  = "alinas"
)

func init() {
	server.RegisterMountHandler(&MountHandler{
		mounter: mount.New(""),
	}, fstypeCpfsNfs, fstypeAlinas)
}

type MountHandler struct {
	mounter mount.Interface
}

func (h *MountHandler) Mount(ctx context.Context, req *proxy.MountRequest) error {
	klog.InfoS("Mounting", "fstype", req.Fstype, "source", req.Source, "target", req.Target, "options", req.Options)
	options := append(req.Options, "no_start_watchdog")
	if req.Fstype == fstypeAlinas {
		options = append(options, "no_atomic_move", "auto_fallback_nfs")
	}
	return h.mounter.Mount(req.Source, req.Target, req.Fstype, options)
}

func (h *MountHandler) Init() {
	setupDefaultConfigs()
	go runLoop("aliyun-alinas-mount-watchdog")
	go runLoop("aliyun-cpfs-mount-watchdog")
}

func (h *MountHandler) Terminate() {}

func runLoop(command string, args ...string) {
	wait.Forever(func() {
		klog.InfoS("Starting", "command", command)
		cmd := exec.Command(command, args...)
		err := cmd.Run()
		if err != nil {
			klog.ErrorS(err, "Exited", "command", command)
		}
	}, time.Second)
}

const (
	defaultConfigDir = "/etc/aliyun-defaults"
	configDir        = "/etc/aliyun"
)

func setupDefaultConfigs() {
	for _, name := range []string{"cpfs", "alinas"} {
		srcDir := filepath.Join(defaultConfigDir, name)
		dstDir := filepath.Join(configDir, name)
		if err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if d.IsDir() {
				return nil
			}

			relPath, err := filepath.Rel(srcDir, path)
			if err != nil {
				return err
			}

			dstPath := filepath.Join(dstDir, relPath)

			if _, err := os.Stat(dstPath); err == nil {
				// File already exists, skip
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}

			klog.InfoS("Copying default config file", "path", dstPath)
			return copyFile(path, dstPath)
		}); err != nil {
			panic(err)
		}
	}
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return err
	}

	return dstFile.Sync()
}
