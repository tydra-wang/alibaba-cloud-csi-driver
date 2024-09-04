package io

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const defaultSysfsPath = "/sys"

var (
	SysConfigNotAllowedErr = errors.New("sysConfig key not allowed")
)

// ParseSysConfigs accepts s in the format 'key1=value1,key2=value2'.
func ParseSysConfigs(s string, allow func(key string) bool) (map[string]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	result := make(map[string]string)
	for _, kv := range strings.Split(s, ",") {
		key, value, found := strings.Cut(kv, "=")
		if !found || key == "" || value == "" {
			return nil, errors.New("invalid sysconfig format")
		}
		// Prevent users from accessing unexpected files through:
		// 1. "subsystem" symlinks like /sys/block/vda/subsystem/vdb
		// 2. relative paths containing ".." like ../../../root/.ssh/id_rsa
		if strings.Contains(key, "..") || strings.Contains(key, "subsystem") || !allow(key) {
			return nil, SysConfigNotAllowedErr
		}
		result[key] = value
	}
	return result, nil
}

type SysConfigManager struct {
	sysfsPath    string
	nonBlockFs   bool
	major, minor uint32
}

func NewSysConfigManagerForDisk(device string) (*SysConfigManager, error) {
	devices := &RealDevTmpFS{}
	major, minor, err := devices.DevFor(device)
	if err != nil {
		return nil, err
	}
	return &SysConfigManager{
		sysfsPath: defaultSysfsPath,
		major:     major,
		minor:     minor,
	}, nil
}

func NewSysConfigManagerForNFS(mountPath string) (*SysConfigManager, error) {
	var stat unix.Statx_t
	err := unix.Statx(0, mountPath, 0, unix.STATX_BASIC_STATS, &stat)
	if err != nil {
		return nil, err
	}

	// check the filesystem is NFS
	major, minor := stat.Dev_major, stat.Dev_minor
	if major != 0 {
		return nil, errors.New("unexpected nfs dev major")
	}
	var statfs unix.Statfs_t
	err = unix.Statfs(mountPath, &statfs)
	if err != nil {
		return nil, err
	}
	if statfs.Type != unix.NFS_SUPER_MAGIC {
		return nil, fmt.Errorf("%s is not a nfs mountpoint", mountPath)
	}

	return &SysConfigManager{
		sysfsPath:  defaultSysfsPath,
		nonBlockFs: true,
		major:      major,
		minor:      minor,
	}, nil
}

func (m *SysConfigManager) SetMulti(kvs map[string]string) error {
	for key, value := range kvs {
		if err := m.Set(key, value); err != nil {
			return fmt.Errorf("set %s=%s failed: %w", key, value, err)
		}
	}
	return nil
}

// Set the sysconfig using a key and value.
// - For block devices, use the path: /sys/dev/block/<major:minor>/<key>
// - For non-block filesystems such as NFS, only keys starting with "bdi/" are supported; use the path: /sys/class/bdi/<major:minor>/<key-suffix>
func (m *SysConfigManager) Set(key, value string) error {
	path, err := m.sysfsPathForKey(key)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	currentValue := strings.TrimSpace(string(data))

	if currentValue == value {
		return nil
	}

	err = WriteTrunc(unix.AT_FDCWD, path, []byte(value))
	if err != nil {
		return err
	}

	logrus.Infof("Set %s from %s to %s", path, currentValue, value)
	return nil
}

func (m *SysConfigManager) sysfsPathForKey(key string) (string, error) {
	devNum := fmt.Sprintf("%d:%d", m.major, m.minor)
	if m.nonBlockFs {
		bdiKey := strings.TrimPrefix(key, "bdi/")
		// For non-block filesystems which provide their own BDI such as NFS and FUSE, only bdi configurations can be set.
		if !strings.HasPrefix(key, "bdi/") {
			return "", SysConfigNotAllowedErr
		}
		return filepath.Join(m.sysfsPath, "class/bdi", devNum, bdiKey), nil
	}
	return filepath.Join(m.sysfsPath, "dev/block", devNum, key), nil
}
