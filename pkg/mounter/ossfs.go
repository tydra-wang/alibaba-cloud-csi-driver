package mounter

import (
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	mountutils "k8s.io/mount-utils"
	"k8s.io/utils/pointer"
)

var FuseOssfs FuseMounterType = &fuseOssfs{}

type fuseOssfs struct{}

func (f *fuseOssfs) name() string {
	return "ossfs"
}

func (f *fuseOssfs) buildPodSpec(
	source, target, fstype string, options, mountFlags []string, nodeName string, config FuseContainerConfig,
) (spec corev1.PodSpec, _ error) {
	if config.Image == "" {
		return spec, errors.New("missing image configuration")
	}

	targetVolume := corev1.Volume{
		Name: "kubelet-dir",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: target,
				Type: new(corev1.HostPathType),
			},
		},
	}
	*targetVolume.HostPath.Type = corev1.HostPathDirectory
	metricsDirVolume := corev1.Volume{
		Name: "metrics-dir",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/var/run/ossfs",
				Type: new(corev1.HostPathType),
			},
		},
	}
	*metricsDirVolume.HostPath.Type = corev1.HostPathDirectoryOrCreate
	passwdFileVolume := corev1.Volume{
		Name: "passwd-ossfs",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/etc/passwd-ossfs",
				Type: new(corev1.HostPathType),
			},
		},
	}
	*passwdFileVolume.HostPath.Type = corev1.HostPathFileOrCreate
	spec.Volumes = []corev1.Volume{targetVolume, metricsDirVolume, passwdFileVolume}

	switch dbglevel := config.Extra["dbglevel"]; dbglevel {
	case "info", "warn", "err", "crit":
		alreadySet := false
		for _, option := range options {
			if strings.Contains(option, "dbglevel") {
				alreadySet = true
				break
			}
		}
		if !alreadySet {
			options = append(options, "dbglevel="+dbglevel)
		}
	default:
		return spec, fmt.Errorf("unknown ossfs dbglevel: %q", dbglevel)
	}
	args := mountutils.MakeMountArgs(source, target, "", options)
	args = append(args, mountFlags...)
	// FUSE foreground option - do not run as daemon
	args = append(args, "-f")
	bidirectional := corev1.MountPropagationBidirectional
	container := corev1.Container{
		Name:      "fuse-mounter",
		Image:     config.Image,
		Args:      args,
		Resources: config.Resources,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:             targetVolume.Name,
				MountPath:        target,
				MountPropagation: &bidirectional,
			}, {
				Name:      metricsDirVolume.Name,
				MountPath: metricsDirVolume.HostPath.Path,
			}, {
				Name:      passwdFileVolume.Name,
				MountPath: passwdFileVolume.HostPath.Path,
			},
		},
		StartupProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"findmnt", "-t", "fuse.ossfs", target,
					},
				},
			},
			PeriodSeconds: 1,
		},
		SecurityContext: &corev1.SecurityContext{
			Privileged: pointer.BoolPtr(true),
		},
	}
	spec.Containers = []corev1.Container{container}
	spec.RestartPolicy = corev1.RestartPolicyNever
	spec.NodeName = nodeName
	spec.HostNetwork = true
	spec.PriorityClassName = "system-node-critical"
	return
}