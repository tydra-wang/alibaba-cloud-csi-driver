package internal

import (
	"os"
	"strings"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	aliyunep "github.com/aliyun/alibaba-cloud-sdk-go/sdk/endpoints"
	nassdk "github.com/aliyun/alibaba-cloud-sdk-go/services/nas"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
)

func init() {
	for _, region := range []string{
		"cn-hangzhou",
		"cn-zhangjiakou",
		"cn-huhehaote",
		"cn-shenzhen",
		"ap-southeast-1",
		"ap-southeast-2",
		"ap-southeast-3",
		"ap-southeast-5",
		"eu-central-1",
		"us-east-1",
		"ap-northeast-1",
		"ap-south-1",
		"us-west-1",
		"eu-west-1",
		"cn-chengdu",
		"cn-north-2-gov-1",
		"cn-beijing",
		"cn-shanghai",
		"cn-hongkong",
		"cn-shenzhen-finance-1",
		"cn-shanghai-finance-1",
		"cn-hangzhou-finance",
		"cn-qingdao",
	} {
		_ = aliyunep.AddEndpointMapping(region, "Nas", "nas-vpc."+region+".aliyuncs.com")
	}
}

func newNasClientV1(region string) (*nassdk.Client, error) {
	if ep := os.Getenv("NAS_ENDPOINT"); ep != "" {
		_ = aliyunep.AddEndpointMapping(region, "Nas", ep)
	}

	ac := utils.GetAccessControl()
	config := ac.Config
	if config == nil {
		config = sdk.NewConfig()
	}
	scheme := strings.ToUpper(os.Getenv("ALICLOUD_CLIENT_SCHEME"))
	if scheme != "HTTP" {
		scheme = "HTTPS"
	}
	config = config.WithScheme(scheme)
	return nassdk.NewClientWithOptions(region, config, ac.Credential)
}
