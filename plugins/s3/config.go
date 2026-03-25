package s3

type S3 struct {
	Port int  `json:"port" env:"PORT"`
	SSL  bool `json:"ssl" env:"SSL"`
}
