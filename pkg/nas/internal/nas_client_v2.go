package internal

import (
	"errors"
	"fmt"
	"os"
	"strings"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	sdk "github.com/alibabacloud-go/nas-20170626/v3/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/sirupsen/logrus"
	"go.uber.org/ratelimit"
)

func newNasClientV2(region string) (*sdk.Client, error) {
	config := new(openapi.Config)
	// set credential
	cred, err := utils.GetCredentialV2()
	if err != nil {
		return nil, fmt.Errorf("init credential: %w", err)
	}
	config = config.SetRegionId(region).SetCredential(cred)
	// set endpoint
	ep := os.Getenv("NAS_ENDPOINT")
	if ep == "" {
		ep = fmt.Sprintf("nas-vpc.%s.aliyuncs.com", region)
	}
	config = config.SetEndpoint(ep)
	// set protocol
	scheme := strings.ToUpper(os.Getenv("ALICLOUD_CLIENT_SCHEME"))
	if scheme != "HTTP" {
		scheme = "HTTPS"
	}
	config = config.SetProtocol(scheme)
	// init client
	return sdk.NewClient(config)
}

type NasClientV2 struct {
	region  string
	limiter ratelimit.Limiter
	client  *sdk.Client
}

// TODO: ensure idempotence
func (c *NasClientV2) CreateDir(req *sdk.CreateDirRequest) error {
	c.limiter.Take()
	resp, err := c.client.CreateDir(req)
	log := logrus.WithFields(logrus.Fields{
		"request":  req,
		"response": resp,
	})
	if err == nil {
		log.Info("nas:CreateDir succeeded")
	} else {
		log.Errorf("nas:CreateDir failed: %v", err)
	}
	return err
}

func (c *NasClientV2) SetDirQuota(req *sdk.SetDirQuotaRequest) error {
	c.limiter.Take()
	resp, err := c.client.SetDirQuota(req)
	if err == nil && resp.Body != nil && !tea.BoolValue(resp.Body.Success) {
		err = errors.New("response indicates a failure")
	}
	log := logrus.WithFields(logrus.Fields{
		"request":  req,
		"response": resp,
	})
	if err == nil {
		log.Info("nas:SetDirQuota succeeded")
	} else {
		log.Errorf("nas:SetDirQuota failed: %v", err)
	}
	return err
}

func (c *NasClientV2) CancelDirQuota(req *sdk.CancelDirQuotaRequest) error {
	c.limiter.Take()
	resp, err := c.client.CancelDirQuota(req)
	if err == nil {
		if !tea.BoolValue(resp.Body.Success) {
			err = errors.New("response indicates a failure")
		}
	} else {
		_err, ok := err.(*tea.SDKError)
		if ok && tea.StringValue(_err.Code) == "InvalidParameter.QuotaNotExistOnPath" {
			// ignore err if quota not exists
			err = nil
		}
	}
	log := logrus.WithFields(logrus.Fields{
		"request":  req,
		"response": resp,
	})
	if err == nil {
		log.Info("nas:CancelDirQuota succeeded")
	} else {
		log.Errorf("nas:CancelDirQuota failed: %v", err)
	}
	return err
}

func (c *NasClientV2) DescribeFileSystemBriefInfo(filesystemId string) (*sdk.DescribeFileSystemBriefInfosResponse, error) {
	c.limiter.Take()
	req := &sdk.DescribeFileSystemBriefInfosRequest{
		FileSystemId: &filesystemId,
	}
	return c.client.DescribeFileSystemBriefInfos(req)
}

func (c *NasClientV2) GetRecycleBinAttribute(filesystemId string) (*sdk.GetRecycleBinAttributeResponse, error) {
	c.limiter.Take()
	req := &sdk.GetRecycleBinAttributeRequest{FileSystemId: &filesystemId}
	return c.client.GetRecycleBinAttribute(req)
}
