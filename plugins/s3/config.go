package s3

type S3 struct {
	Address  string `json:"address" env:"ADDR"`
	Port     int    `json:"port" env:"PORT"`
	SSL      bool   `json:"ssl" env:"SSL"`
	CertFile string `json:"cert_file" env:"CERT_FILE"`
	KeyFile  string `json:"key_file" env:"KEY_FILE"`
}
