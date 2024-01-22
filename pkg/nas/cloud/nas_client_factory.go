package cloud

import (
	sdkv1 "github.com/aliyun/alibaba-cloud-sdk-go/services/nas"
	"go.uber.org/ratelimit"
)

type NasClientFactory struct {
	// ratelimiter only takes effect on v2 client
	limiter ratelimit.Limiter
}

func NewNasClientFactory(nasQpsLimit int) *NasClientFactory {
	return &NasClientFactory{
		limiter: ratelimit.New(nasQpsLimit),
	}
}

func (fac *NasClientFactory) V2(region string) (*NasClientV2, error) {
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

func (fac *NasClientFactory) V1(region string) (*sdkv1.Client, error) {
	return newNasClientV1(region)
}
