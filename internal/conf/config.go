package conf

import (
	"encoding/json"
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

type PluginConfig struct {
	Name   string         `json:"name"`
	Enable bool           `json:"enable"`
	Data   map[string]any `json:"data"`
}

type Config struct {
	Force                 bool           `json:"force" env:"FORCE"`
	SiteURL               string         `json:"site_url" env:"SITE_URL"`
	Cdn                   string         `json:"cdn" env:"CDN"`
	JwtSecret             string         `json:"jwt_secret" env:"JWT_SECRET"`
	TokenExpiresIn        int            `json:"token_expires_in" env:"TOKEN_EXPIRES_IN"`
	Database              Database       `json:"database" envPrefix:"DB_"`
	Meilisearch           Meilisearch    `json:"meilisearch" envPrefix:"MEILISEARCH_"`
	TempDir               string         `json:"temp_dir" env:"TEMP_DIR"`
	BleveDir              string         `json:"bleve_dir" env:"BLEVE_DIR"`
	DistDir               string         `json:"dist_dir"`
	Log                   LogConfig      `json:"log" envPrefix:"LOG_"`
	DelayedStart          int            `json:"delayed_start" env:"DELAYED_START"`
	MaxBufferLimit        int            `json:"max_buffer_limitMB" env:"MAX_BUFFER_LIMIT_MB"`
	MmapThreshold         int            `json:"mmap_thresholdMB" env:"MMAP_THRESHOLD_MB"`
	MaxConnections        int            `json:"max_connections" env:"MAX_CONNECTIONS"`
	MaxConcurrency        int            `json:"max_concurrency" env:"MAX_CONCURRENCY"`
	TlsInsecureSkipVerify bool           `json:"tls_insecure_skip_verify" env:"TLS_INSECURE_SKIP_VERIFY"`
	Tasks                 TasksConfig    `json:"tasks" envPrefix:"TASKS_"`
	Cors                  Cors           `json:"cors" envPrefix:"CORS_"`
	Plugins               []PluginConfig `json:"plugins"`
	LastLaunchedVersion   string         `json:"last_launched_version"`
	ProxyAddress          string         `json:"proxy_address" env:"PROXY_ADDRESS"`
}

func DefaultConfig(dataDir string) *Config {
	tempDir := filepath.Join(dataDir, "temp")
	indexDir := filepath.Join(dataDir, "bleve")
	logPath := filepath.Join(dataDir, "log/log.log")
	dbPath := filepath.Join(dataDir, "data.db")
	return &Config{
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
		MmapThreshold:         4,
		MaxConnections:        0,
		MaxConcurrency:        64,
		TlsInsecureSkipVerify: false,
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
		Plugins: []PluginConfig{
			{
				Name:   "server",
				Enable: true,
				Data: map[string]any{
					"address":        "0.0.0.0",
					"http_port":      5244,
					"https_port":     -1,
					"force_https":    false,
					"cert_file":      "",
					"key_file":       "",
					"unix_file":      "",
					"unix_file_perm": "",
					"enable_h2c":     false,
					"enable_h3":      false,
				},
			},
			{
				Name:   "webdav",
				Enable: false,
				Data: map[string]any{
					"listen": ":5288",
					"ssl":    false,
				},
			},
			{
				Name:   "s3",
				Enable: false,
				Data: map[string]any{
					"address":   "0.0.0.0",
					"port":      5246,
					"ssl":       false,
					"cert_file": "",
					"key_file":  "",
				},
			},
			{
				Name:   "ftp",
				Enable: false,
				Data: map[string]any{
					"listen":                      ":5221",
					"find_pasv_port_attempts":     50,
					"active_transfer_port_non_20": false,
					"idle_timeout":                900,
					"connection_timeout":          30,
					"disable_active_mode":         false,
					"default_transfer_binary":     false,
					"enable_active_conn_ip_check": true,
					"enable_pasv_conn_ip_check":   true,
				},
			},
			{
				Name:   "sftp",
				Enable: false,
				Data: map[string]any{
					"listen": ":5222",
				},
			},
		},
		LastLaunchedVersion: "",
		ProxyAddress:        "",
	}
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type configAlias Config
	if err := json.Unmarshal(data, (*configAlias)(c)); err != nil {
		return err
	}
	for i := range c.Plugins {
		if c.Plugins[i].Data == nil {
			c.Plugins[i].Data = map[string]any{}
		}
	}
	return nil
}

func (c *Config) Plugin(name string) *PluginConfig {
	for i := range c.Plugins {
		if c.Plugins[i].Name == name {
			return &c.Plugins[i]
		}
	}
	return nil
}
