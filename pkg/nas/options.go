package nas

const (
	// NasTempMntPath used for create sub directory
	NasTempMntPath = "/mnt/acs_mnt/k8s_nas/temp"
	// NasPortnum is nas port
	NasPortnum = "2049"
	// NasMetricByPlugin tag
	NasMetricByPlugin = "NAS_METRIC_BY_PLUGIN"
	// MixRunTimeMode support both runc and runv
	MixRunTimeMode = "runc-runv"
	// RunvRunTimeMode tag
	RunvRunTimeMode = "runv"
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
	// EFCClient
	EFCClient = "efcclient"
	// NFSClient
	NFSClient = "nfsclient"
	// NativeClient
	NativeClient = "nativeclient"
)

// Options struct definition
type Options struct {
	Server        string `json:"server"`
	Path          string `json:"path"`
	Vers          string `json:"vers"`
	Mode          string `json:"mode"`
	ModeType      string `json:"modeType"`
	Options       string `json:"options"`
	MountType     string `json:"mountType"`
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

func (o Options) PrepareMountOptions(pvName, podUID string) (string, []string) {
	switch o.ClientType {
	case EFCClient:
	case NativeClient:
	default:
	}
	return o., nil
}
