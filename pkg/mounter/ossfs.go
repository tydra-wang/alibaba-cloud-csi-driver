package mounter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alibabacloud-go/tea/tea"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	mountutils "k8s.io/mount-utils"
)

var defaultOssfsImageTag = "4af1b0e-aliyun"

const (
	hostPrefix                = "/host"
	OssfsCredentialSecretName = "csi-ossfs-credentials"
	OssfsDefMimeTypesFilePath = "/etc/mime.types"
	OssfsCsiMimeTypesFilePath = "/etc/csi-mime.types"

	defaultRegistry = "registry-cn-hangzhou.ack.aliyuncs.com"

	CsiSecretStoreDriver   = "secrets-store.csi.k8s.io"
	SecretProviderClassKey = "secretProviderClass"
)

type fuseOssfs struct {
	config FuseContainerConfig
}

func NewFuseOssfs(configmap *corev1.ConfigMap, m metadata.MetadataProvider) FuseMounterType {
	config := extractFuseContainerConfig(configmap, "ossfs")
	// set default image
	if config.Image == "" {
		registry := os.Getenv("DEFAULT_REGISTRY")
		if registry == "" {
			region, err := m.Get(metadata.RegionID)
			if err == nil {
				registry = fmt.Sprintf("registry-%s-vpc.ack.aliyuncs.com", region)
			} else {
				log.Warnf("DEFAULT_REGISTRY env not set, failed to get current region: %v, fallback to default registry: %s", err, defaultRegistry)
				registry = defaultRegistry
			}
		}
		config.Image = fmt.Sprintf("%s/acs/csi-ossfs:%s", registry, defaultOssfsImageTag)
	}
	// set default memory request
	if _, ok := config.Resources.Requests[corev1.ResourceMemory]; !ok {
		config.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("50Mi")
	}

	return &fuseOssfs{config: config}
}

func (f *fuseOssfs) name() string {
	return "ossfs"
}

func (f *fuseOssfs) addPodMeta(pod *corev1.Pod) {
	if pod == nil {
		return
	}
	if f.config.Annotations != nil && pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	for k, v := range f.config.Annotations {
		pod.Annotations[k] = v
	}
	if f.config.Labels != nil && pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	for k, v := range f.config.Labels {
		pod.Labels[k] = v
	}
}

func (f *fuseOssfs) buildPodSpec(
	source, target, fstype string, authCfg *AuthConfig, options, mountFlags []string, nodeName, volumeId string,
) (spec corev1.PodSpec, _ error) {
	spec.TerminationGracePeriodSeconds = tea.Int64(120)
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
	spec.Volumes = []corev1.Volume{targetVolume, metricsDirVolume}

	var mimeMountDir string
	if utils.IsFileExisting(filepath.Join(hostPrefix, OssfsDefMimeTypesFilePath)) {
		// mime.types already exists on host
		mimeMountDir = OssfsDefMimeTypesFilePath
	} else if strings.ToLower(f.config.Extra["mime-support"]) == "true" {
		// mime.types not exists, use csi-mime.types
		options = append(options, fmt.Sprintf("mime=%s", OssfsCsiMimeTypesFilePath))
		mimeMountDir = OssfsCsiMimeTypesFilePath
	}

	mimeDirVolume := corev1.Volume{
		Name: "mime-types-dir",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: mimeMountDir,
				Type: new(corev1.HostPathType),
			},
		},
	}
	*mimeDirVolume.HostPath.Type = corev1.HostPathFile
	if mimeMountDir == OssfsDefMimeTypesFilePath {
		spec.Volumes = append(spec.Volumes, mimeDirVolume)
	}

	switch dbglevel := f.config.Extra["dbglevel"]; dbglevel {
	case "":
	case "debug", "info", "warn", "err", "crit":
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
		return spec, fmt.Errorf("invalid ossfs dbglevel: %q", dbglevel)
	}
	bidirectional := corev1.MountPropagationBidirectional
	container := corev1.Container{
		Name:      "fuse-mounter",
		Image:     f.config.Image,
		Resources: f.config.Resources,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:             targetVolume.Name,
				MountPath:        target,
				MountPropagation: &bidirectional,
			}, {
				Name:      metricsDirVolume.Name,
				MountPath: metricsDirVolume.HostPath.Path,
			},
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"findmnt", "-t", "fuse.ossfs", target,
					},
				},
			},
			PeriodSeconds:    1,
			FailureThreshold: 5,
		},
		SecurityContext: &corev1.SecurityContext{
			Privileged: tea.Bool(true),
		},
	}
	if mimeMountDir == OssfsDefMimeTypesFilePath {
		mimeVolumeMount := corev1.VolumeMount{
			Name:      mimeDirVolume.Name,
			MountPath: mimeMountDir,
		}
		container.VolumeMounts = append(container.VolumeMounts, mimeVolumeMount)
	}

	buildAuthSpec(nodeName, volumeId, target, authCfg, &spec, &container, &options)

	args := mountutils.MakeMountArgs(source, target, "", options)
	args = append(args, mountFlags...)
	// FUSE foreground option - do not run as daemon
	args = append(args, "-f")
	container.Args = args

	spec.Containers = []corev1.Container{container}
	spec.RestartPolicy = corev1.RestartPolicyOnFailure
	spec.NodeName = nodeName
	spec.HostNetwork = true
	spec.PriorityClassName = "system-node-critical"
	spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	return
}

func SetupOssfsCredentialSecret(ctx context.Context, clientset kubernetes.Interface, node, volumeId, bucket, akId, akSecret string) error {
	key := fmt.Sprintf("%s.%s", node, volumeId)
	value := fmt.Sprintf("%s:%s:%s", bucket, akId, akSecret)
	secretClient := clientset.CoreV1().Secrets(fuseMountNamespace)
	secret, err := secretClient.Get(ctx, OssfsCredentialSecretName, metav1.GetOptions{})
	if err != nil {
		// if secret not found, create it
		if errors.IsNotFound(err) {
			secret = new(corev1.Secret)
			secret.Name = OssfsCredentialSecretName
			secret.Namespace = fuseMountNamespace
			secret.Data = map[string][]byte{key: []byte(value)}
			_, err = secretClient.Create(ctx, secret, metav1.CreateOptions{})
			if err == nil {
				log.WithField("volumeId", volumeId).Infof("created secret %s to add credentials", OssfsCredentialSecretName)
			}
			return err
		}
		return err
	}
	if string(secret.Data[key]) == value {
		return nil
	}
	// patch secret
	patch := corev1.Secret{
		Data: map[string][]byte{
			key: []byte(value),
		},
	}
	patchData, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = secretClient.Patch(ctx, OssfsCredentialSecretName, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
	if err == nil {
		log.WithField("volumeId", volumeId).Infof("patched secret %s", OssfsCredentialSecretName)
	}
	return err
}

func CleanupOssfsCredentialSecret(ctx context.Context, clientset kubernetes.Interface, node, volumeId string) error {
	key := fmt.Sprintf("%s.%s", node, volumeId)
	secretClient := clientset.CoreV1().Secrets(fuseMountNamespace)
	secret, err := secretClient.Get(ctx, OssfsCredentialSecretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	_, exists := secret.Data[key]
	if !exists {
		return nil
	}
	// patch secret
	patch := corev1.Secret{
		Data: map[string][]byte{
			key: nil,
		},
	}
	patchData, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = secretClient.Patch(ctx, OssfsCredentialSecretName, types.StrategicMergePatchType, patchData, metav1.PatchOptions{})
	if err == nil {
		log.WithField("volumeId", volumeId).Infof("patched secret %s to remove credentials", OssfsCredentialSecretName)
	}
	return err
}

func buildAuthSpec(nodeName, volumeId, target string, authCfg *AuthConfig,
	spec *corev1.PodSpec, container *corev1.Container, options *[]string) {
	if spec == nil || container == nil || options == nil {
		return
	}
	if authCfg == nil {
		authCfg = &AuthConfig{}
	}

	switch authCfg.AuthType {
	case AuthTypeSTS:
	case AuthTypeRRSA:
		if authCfg.RrsaConfig == nil {
			return
		}
		spec.ServiceAccountName = authCfg.RrsaConfig.ServiceAccountName
		rrsaMountDir := "/var/run/secrets/ack.alibabacloud.com/rrsa-tokens"
		rrsaVolume := corev1.Volume{
			Name: "rrsa-oidc-token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					DefaultMode: tea.Int32(0600),
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          "sts.aliyuncs.com",
								ExpirationSeconds: tea.Int64(3600),
								Path:              "token",
							},
						},
					},
				},
			},
		}
		spec.Volumes = append(spec.Volumes, rrsaVolume)
		rrsaVolumeMount := corev1.VolumeMount{
			Name:      rrsaVolume.Name,
			MountPath: rrsaMountDir,
		}
		container.VolumeMounts = append(container.VolumeMounts, rrsaVolumeMount)
		envs := []corev1.EnvVar{
			{
				Name:  "ALIBABA_CLOUD_ROLE_ARN",
				Value: authCfg.RrsaConfig.RoleArn,
			},
			{
				Name:  "ALIBABA_CLOUD_OIDC_PROVIDER_ARN",
				Value: authCfg.RrsaConfig.OidcProviderArn,
			},
			{
				Name:  "ALIBABA_CLOUD_OIDC_TOKEN_FILE",
				Value: rrsaMountDir + "/token",
			},
			{
				Name:  "ROLE_SESSION_NAME",
				Value: getRoleSessionName(volumeId, target),
			},
		}
		container.Env = envs
	case AuthTypeCSS:
		secretStoreMountDir := "/etc/ossfs/secrets-store"
		secretStoreVolume := corev1.Volume{
			Name: "secrets-store",
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver:           CsiSecretStoreDriver,
					ReadOnly:         tea.Bool(true),
					VolumeAttributes: map[string]string{SecretProviderClassKey: authCfg.SecretProviderClassName},
				},
			},
		}
		spec.Volumes = append(spec.Volumes, secretStoreVolume)
		secretStoreVolumeMount := corev1.VolumeMount{
			Name:      secretStoreVolume.Name,
			MountPath: secretStoreMountDir,
			ReadOnly:  true,
		}
		container.VolumeMounts = append(container.VolumeMounts, secretStoreVolumeMount)
		*options = append(*options, fmt.Sprintf("secret_store_dir=%s", secretStoreMountDir))
	default:
		passwdMountDir := "/etc/ossfs"
		passwdFilename := "passwd-ossfs"
		passwdSecretVolume := corev1.Volume{
			Name: "passwd-ossfs",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: OssfsCredentialSecretName,
					Items: []corev1.KeyToPath{
						{
							Key:  fmt.Sprintf("%s.%s", nodeName, volumeId),
							Path: passwdFilename,
							Mode: tea.Int32(0600),
						},
					},
					Optional: tea.Bool(true),
				},
			},
		}
		spec.Volumes = append(spec.Volumes, passwdSecretVolume)
		passwdVolumeMont := corev1.VolumeMount{
			Name:      passwdSecretVolume.Name,
			MountPath: passwdMountDir,
		}
		container.VolumeMounts = append(container.VolumeMounts, passwdVolumeMont)
		*options = append(*options, fmt.Sprintf("passwd_file=%s", filepath.Join(passwdMountDir, passwdFilename)))
	}
}
