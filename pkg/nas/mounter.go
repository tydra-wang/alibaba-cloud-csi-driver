package nas

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	mountutils "k8s.io/mount-utils"
)

type NasMounter struct {
	mountutils.Interface
	fuseMounter mountutils.Interface
}

func (m *NasMounter) Mount(source string, target string, fstype string, options []string) error {
	switch fstype {
	case "alinas", "cpfs", "cpfs-nfs":
		return m.fuseMounter.Mount(source, target, fstype, options)
	default:
		return m.Interface.Mount(source, target, fstype, options)
	}
}

func (m *NasMounter) Unmount(target string) error {
	cpfs, err := m.isCpfsMountpoint(target)
	if err != nil {
		return err
	}
	if cpfs {
		return m.unmountCpfs(target)
	}
	return m.Interface.Unmount(target)
}

func (m *NasMounter) UnmountWithForce(target string, umountTimeout time.Duration) error {
	cpfs, err := m.isCpfsMountpoint(target)
	if err != nil {
		return err
	}
	if cpfs {
		return m.unmountCpfs(target)
	}
	return m.Interface.(mountutils.MounterForceUnmounter).UnmountWithForce(target, umountTimeout)
}

func (m *NasMounter) isCpfsMountpoint(target string) (bool, error) {
	fstype, found, err := mounter.GetMountpointType(m, target)
	if err != nil {
		return false, fmt.Errorf("failed to determine fstype: %w", err)
	}
	if !found {
		return false, fmt.Errorf("not a mountpoint: %q", target)
	}
	return fstype == "gpfs", nil
}

func (m *NasMounter) unmountCpfs(target string) error {
	cmd := "mount.cpfs --version"
	out, err := utils.ConnectorRun(cmd)
	if out != "" {
		log.Infof("ConnectorRun: %s, output: %q", cmd, out)
	}
	if err != nil {
		return fmt.Errorf("check mount.cpfs version: %w", err)
	}
	needHelper, err := isCpfsVersionLargerThen1_1_5(out)
	if err != nil {
		return err
	}
	if !needHelper {
		return m.Interface.Unmount(target)
	}

	// use mount.cpfs to unmount
	cmd = fmt.Sprintf("mount.cpfs -u %s", target)
	out, err = utils.ConnectorRun(cmd)
	if out != "" {
		log.Infof("ConnectorRun: %s, output: %q", cmd, out)
	}
	if err != nil {
		return fmt.Errorf("unmount cpfs: %w", err)
	}
	return nil
}

func isCpfsVersionLargerThen1_1_5(versionOutput string) (bool, error) {
	re := regexp.MustCompile(`Version: (\d+)\.(\d+)-(\d+)$`)
	match := re.FindStringSubmatch(versionOutput)
	if len(match) == 4 {
		majorVersion, err := strconv.Atoi(match[1])
		valid := err == nil
		minorVersion, err := strconv.Atoi(match[2])
		valid = valid && err == nil
		revision, err := strconv.Atoi(match[3])
		valid = valid && err == nil
		if !valid {
			return false, errors.New("unexpected mount.cpfs version format")
		}
		baseVersion := []int{1, 1, 5}
		for i, v := range []int{majorVersion, minorVersion, revision} {
			if v > baseVersion[i] {
				return true, nil
			} else if v < baseVersion[i] {
				return false, nil
			}
		}
		return false, nil
	}
	return false, errors.New("unexpected output of mount.cpfs --version")
}

func NewNasMounter() mountutils.Interface {
	inner := mountutils.New("")
	return &NasMounter{
		Interface:   inner,
		fuseMounter: mounter.NewConnectorMounter(inner),
	}
}
