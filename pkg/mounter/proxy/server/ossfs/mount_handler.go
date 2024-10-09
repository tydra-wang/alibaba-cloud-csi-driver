package ossfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

func init() {
	server.RegisterMountHandler(NewMountHandler(), "ossfs")
}

type MountHandler struct {
	pids *sync.Map
	wg   sync.WaitGroup
	raw  mount.Interface
}

func NewMountHandler() *MountHandler {
	return &MountHandler{
		pids: new(sync.Map),
		raw:  mount.New(""),
	}
}

func (h *MountHandler) Mount(ctx context.Context, req *proxy.MountRequest) error {
	options := req.Options

	// prepare passwd file
	var passwdFile string
	if passwd := req.Secrets["passwd-ossfs"]; passwd != "" {
		tmpDir, err := os.MkdirTemp("", "ossfs-")
		if err != nil {
			return err
		}
		passwdFile = filepath.Join(tmpDir, "passwd")
		err = os.WriteFile(passwdFile, []byte(passwd), 0o600)
		if err != nil {
			return err
		}
		klog.V(4).InfoS("created ossfs passwd file", "path", passwdFile)
		options = append(options, "passwd_file="+passwdFile)
	}

	args := mount.MakeMountArgs(req.Source, req.Target, "", options)
	args = append(args, req.MountFlags...)
	args = append(args, "-f")

	cmd := exec.Command("ossfs", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("start ossfs failed: %w", err)
	}

	target := req.Target
	pid := cmd.Process.Pid
	klog.InfoS("Started ossfs", "pid", pid, "args", args)

	ossfsExited := make(chan error, 1)
	h.wg.Add(1)
	h.pids.Store(pid, cmd)
	go func() {
		defer h.wg.Done()
		defer h.pids.Delete(pid)

		err := cmd.Wait()
		if err != nil {
			klog.ErrorS(err, "ossfs exited with error", "mountpoint", target, "pid", pid)
		} else {
			klog.InfoS("ossfs exited", "mountpoint", target, "pid", pid)
		}
		ossfsExited <- err
		if err := os.Remove(passwdFile); err != nil {
			klog.ErrorS(err, "Remove passwd file", "mountpoint", target, "path", passwdFile)
		}
		close(ossfsExited)
	}()

	err = wait.PollInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (done bool, err error) {
		select {
		case err := <-ossfsExited:
			// TODO: collect ossfs outputs to return in error message
			if err != nil {
				return false, fmt.Errorf("ossfs exited: %w", err)
			}
			return false, fmt.Errorf("ossfs exited")
		default:
			notMnt, err := h.raw.IsLikelyNotMountPoint(target)
			if err != nil {
				klog.ErrorS(err, "check mountpoint", "mountpoint", target)
				return false, nil
			}
			if !notMnt {
				klog.InfoS("Successfully mounted", "mountpoint", target)
				return true, nil
			}
			return false, nil
		}
	})

	if err == nil {
		return nil
	}

	if errors.Is(err, wait.ErrWaitTimeout) {
		// terminate ossfs process when timeout
		terr := cmd.Process.Signal(syscall.SIGTERM)
		if terr != nil {
			klog.ErrorS(err, "Failed to terminate ossfs", "pid", pid)
		}
		select {
		case <-ossfsExited:
		case <-time.After(time.Second * 2):
			kerr := cmd.Process.Kill()
			if kerr != nil && errors.Is(kerr, os.ErrProcessDone) {
				klog.ErrorS(err, "Failed to kill ossfs", "pid", pid)
			}
		}
	}
	return err
}

func (h *MountHandler) Init() {}

func (h *MountHandler) Terminate() {
	// terminate all running ossfs
	h.pids.Range(func(key, value any) bool {
		err := value.(*exec.Cmd).Process.Signal(syscall.SIGTERM)
		if err != nil {
			klog.ErrorS(err, "Failed to terminate ossfs", "pid", key)
		}
		klog.V(4).InfoS("Sended sigterm", "pid", key)
		return true
	})
	// wait all ossfs processes to exit
	h.wg.Wait()
	klog.InfoS("All ossfs processes exited")
}
