package net

import "github.com/aliyun/aliyun-oss-go-sdk/oss"

func NewOSSClient(endpoint, accessKeyID, accessKeySecret string, options ...oss.ClientOption) (*oss.Client, error) {
	clientOptions := []oss.ClientOption{oss.HTTPClient(NewHttpClient())}
	clientOptions = append(clientOptions, options...)
	return oss.New(endpoint, accessKeyID, accessKeySecret, clientOptions...)
}
