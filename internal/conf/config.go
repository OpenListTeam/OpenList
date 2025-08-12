package conf

import (
	"path/filepath"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
)

type Database struct {
	Type        string `json:"type" env:"TYPE"`
	Host        string `json:"host" env:"HOST"`
	Port        int    `json:"port" env:"PORT"`
	User        string `json:"user" env:"USER"`
	Password    string `json:"password" env:"PASS"`
	Name        string `json:"name" env:"NAME"`
	DBFile      string `json:"db_file" env:"FILE"`
	TablePrefix string `json:"table_prefix" env:"TABLE_PREFIX"`
	SSLMode     string `json:"ssl_mode" env:"SSL_MODE"`
	DSN         string `json:"dsn" env:"DSN"`
}

type Meilisearch struct {
	Host   string `json:"host" env:"HOST"`
	APIKey string `json:"api_key" env:"API_KEY"`
	Index  string `json:"index" env:"INDEX"`
}

type Scheme struct {
	Address      string `json:"address" env:"ADDR"`
	HttpPort     int    `json:"http_port" env:"HTTP_PORT"`
	HttpsPort    int    `json:"https_port" env:"HTTPS_PORT"`
	ForceHttps   bool   `json:"force_https" env:"FORCE_HTTPS"`
	CertFile     string `json:"cert_file" env:"CERT_FILE"`
	KeyFile      string `json:"key_file" env:"KEY_FILE"`
	UnixFile     string `json:"unix_file" env:"UNIX_FILE"`
	UnixFilePerm string `json:"unix_file_perm" env:"UNIX_FILE_PERM"`
	EnableH2c    bool   `json:"enable_h2c" env:"ENABLE_H2C"`
}

type LogConfig struct {
	Enable     bool            `json:"enable" env:"ENABLE"`
	Name       string          `json:"name" env:"NAME"`
	MaxSize    int             `json:"max_size" env:"MAX_SIZE"`
	MaxBackups int             `json:"max_backups" env:"MAX_BACKUPS"`
	MaxAge     int             `json:"max_age" env:"MAX_AGE"`
	Compress   bool            `json:"compress" env:"COMPRESS"`
	Filter     LogFilterConfig `json:"filter" envPrefix:"FILTER_"`
}

type LogFilterConfig struct {
	Enable  bool     `json:"enable" env:"ENABLE"`
	Filters []Filter `json:"filters"`
}

type Filter struct {
	CIDR   string `json:"cidr"`
	Path   string `json:"path"`
	Method string `json:"method"`
}

type TaskConfig struct {
	Workers        int  `json:"workers" env:"WORKERS"`
	MaxRetry       int  `json:"max_retry" env:"MAX_RETRY"`
	TaskPersistant bool `json:"task_persistant" env:"TASK_PERSISTANT"`
}

type TasksConfig struct {
	Download           TaskConfig `json:"download" envPrefix:"DOWNLOAD_"`
	Transfer           TaskConfig `json:"transfer" envPrefix:"TRANSFER_"`
	Upload             TaskConfig `json:"upload" envPrefix:"UPLOAD_"`
	Copy               TaskConfig `json:"copy" envPrefix:"COPY_"`
	Move               TaskConfig `json:"move" envPrefix:"MOVE_"`
	Decompress         TaskConfig `json:"decompress" envPrefix:"DECOMPRESS_"`
	DecompressUpload   TaskConfig `json:"decompress_upload" envPrefix:"DECOMPRESS_UPLOAD_"`
	AllowRetryCanceled bool       `json:"allow_retry_canceled" env:"ALLOW_RETRY_CANCELED"`
}

type Cors struct {
	AllowOrigins []string `json:"allow_origins" env:"ALLOW_ORIGINS"`
	AllowMethods []string `json:"allow_methods" env:"ALLOW_METHODS"`
	AllowHeaders []string `json:"allow_headers" env:"ALLOW_HEADERS"`
}

type S3 struct {
	Enable bool `json:"enable" env:"ENABLE"`
	Port   int  `json:"port" env:"PORT"`
	SSL    bool `json:"ssl" env:"SSL"`
}

type FTP struct {
	Enable                  bool   `json:"enable" env:"ENABLE"`
	Listen                  string `json:"listen" env:"LISTEN"`
	FindPasvPortAttempts    int    `json:"find_pasv_port_attempts" env:"FIND_PASV_PORT_ATTEMPTS"`
	ActiveTransferPortNon20 bool   `json:"active_transfer_port_non_20" env:"ACTIVE_TRANSFER_PORT_NON_20"`
	IdleTimeout             int    `json:"idle_timeout" env:"IDLE_TIMEOUT"`
	ConnectionTimeout       int    `json:"connection_timeout" env:"CONNECTION_TIMEOUT"`
	DisableActiveMode       bool   `json:"disable_active_mode" env:"DISABLE_ACTIVE_MODE"`
	DefaultTransferBinary   bool   `json:"default_transfer_binary" env:"DEFAULT_TRANSFER_BINARY"`
	EnableActiveConnIPCheck bool   `json:"enable_active_conn_ip_check" env:"ENABLE_ACTIVE_CONN_IP_CHECK"`
	EnablePasvConnIPCheck   bool   `json:"enable_pasv_conn_ip_check" env:"ENABLE_PASV_CONN_IP_CHECK"`
}

type SFTP struct {
	Enable bool   `json:"enable" env:"ENABLE"`
	Listen string `json:"listen" env:"LISTEN"`
}

type Config struct {
	Force                 bool        `json:"force" env:"FORCE"`
	SiteURL               string      `json:"site_url" env:"SITE_URL"`
	Cdn                   string      `json:"cdn" env:"CDN"`
	JwtSecret             string      `json:"jwt_secret" env:"JWT_SECRET"`
	TokenExpiresIn        int         `json:"token_expires_in" env:"TOKEN_EXPIRES_IN"`
	Database              Database    `json:"database" envPrefix:"DB_"`
	Meilisearch           Meilisearch `json:"meilisearch" envPrefix:"MEILISEARCH_"`
	Scheme                Scheme      `json:"scheme"`
	TempDir               string      `json:"temp_dir" env:"TEMP_DIR"`
	BleveDir              string      `json:"bleve_dir" env:"BLEVE_DIR"`
	DistDir               string      `json:"dist_dir"`
	Log                   LogConfig   `json:"log" envPrefix:"LOG_"`
	DelayedStart          int         `json:"delayed_start" env:"DELAYED_START"`
	MaxBufferLimit        int         `json:"max_buffer_limitMB" env:"MAX_BUFFER_LIMIT_MB"`
	MaxConnections        int         `json:"max_connections" env:"MAX_CONNECTIONS"`
	MaxConcurrency        int         `json:"max_concurrency" env:"MAX_CONCURRENCY"`
	TlsInsecureSkipVerify bool        `json:"tls_insecure_skip_verify" env:"TLS_INSECURE_SKIP_VERIFY"`
	Tasks                 TasksConfig `json:"tasks" envPrefix:"TASKS_"`
	Cors                  Cors        `json:"cors" envPrefix:"CORS_"`
	S3                    S3          `json:"s3" envPrefix:"S3_"`
	FTP                   FTP         `json:"ftp" envPrefix:"FTP_"`
	SFTP                  SFTP        `json:"sftp" envPrefix:"SFTP_"`
	LastLaunchedVersion   string      `json:"last_launched_version"`
}

func DefaultConfig(dataDir string) *Config {
	tempDir := filepath.Join(dataDir, "temp")
	indexDir := filepath.Join(dataDir, "bleve")
	logPath := filepath.Join(dataDir, "log/log.log")
	dbPath := filepath.Join(dataDir, "data.db")
	return &Config{
		Scheme: Scheme{
			Address:    "0.0.0.0",
			UnixFile:   "",
			HttpPort:   5244,
			HttpsPort:  -1,
			ForceHttps: false,
			CertFile:   "",
			KeyFile:    "",
		},
		JwtSecret:      random.String(16),
		TokenExpiresIn: 48,
		TempDir:        tempDir,
		Database: Database{
			Type:        "sqlite3",
			Port:        0,
			TablePrefix: "x_",
			DBFile:      dbPath,
		},
		Meilisearch: Meilisearch{
			Host:  "http://localhost:7700",
			Index: "openlist",
		},
		BleveDir: indexDir,
		Log: LogConfig{
			Enable:     true,
			Name:       logPath,
			MaxSize:    50,
			MaxBackups: 30,
			MaxAge:     28,
			Filter: LogFilterConfig{
				Enable: false,
				Filters: []Filter{
					{Path: "/ping"},
					{Method: "HEAD"},
					{Path: "/dav/", Method: "PROPFIND"},
				},
			},
		},
		MaxBufferLimit:        -1,
		MaxConnections:        0,
		MaxConcurrency:        64,
		TlsInsecureSkipVerify: true,
		Tasks: TasksConfig{
			Download: TaskConfig{
				Workers:  5,
				MaxRetry: 1,
				// TaskPersistant: true,
			},
			Transfer: TaskConfig{
				Workers:  5,
				MaxRetry: 2,
				// TaskPersistant: true,
			},
			Upload: TaskConfig{
				Workers: 5,
			},
			Copy: TaskConfig{
				Workers:  5,
				MaxRetry: 2,
				// TaskPersistant: true,
			},
			Move: TaskConfig{
				Workers:  5,
				MaxRetry: 2,
				// TaskPersistant: true,
			},
			Decompress: TaskConfig{
				Workers:  5,
				MaxRetry: 2,
				// TaskPersistant: true,
			},
			DecompressUpload: TaskConfig{
				Workers:  5,
				MaxRetry: 2,
			},
			AllowRetryCanceled: false,
		},
		Cors: Cors{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{"*"},
			AllowHeaders: []string{"*"},
		},
		S3: S3{
			Enable: false,
			Port:   5246,
			SSL:    false,
		},
		FTP: FTP{
			Enable:                  false,
			Listen:                  ":5221",
			FindPasvPortAttempts:    50,
			ActiveTransferPortNon20: false,
			IdleTimeout:             900,
			ConnectionTimeout:       30,
			DisableActiveMode:       false,
			DefaultTransferBinary:   false,
			EnableActiveConnIPCheck: true,
			EnablePasvConnIPCheck:   true,
		},
		SFTP: SFTP{
			Enable: false,
			Listen: ":5222",
		},
		LastLaunchedVersion: "",
	}
}
