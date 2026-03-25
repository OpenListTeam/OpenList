package webdav

type WebDAVConfig struct {
	Listen   string `json:"listen"`
	SSL      bool   `json:"ssl"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}
