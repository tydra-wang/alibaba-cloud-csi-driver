package internal

import (
	sdkv1 "github.com/aliyun/alibaba-cloud-sdk-go/services/nas"
	"go.uber.org/ratelimit"
)

type NasClient struct {
	limiter ratelimit.Limiter
}

func NewNasClient(nasQpsLimit int) *NasClient {
	return &NasClient{
		limiter: ratelimit.New(nasQpsLimit),
	}
}

func (fac *NasClient) V2(region string) (*NasClientV2, error) {
	client, err := newNasClientV2(region)
	if err != nil {
		return nil, err
	}
	return &NasClientV2{
		region:  region,
		limiter: fac.limiter,
		client:  client,
	}, nil
}

func (fac *NasClient) V1(region string) (*sdkv1.Client, error) {
	return newNasClientV1(region)
}
