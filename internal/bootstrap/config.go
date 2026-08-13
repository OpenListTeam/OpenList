package bootstrap

import (
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/caarlos0/env/v9"
	"github.com/shirou/gopsutil/v4/mem"
	log "github.com/sirupsen/logrus"
)

// Program working directory
func PWD() string {
	if flags.ForceBinDir {
		ex, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		pwd := filepath.Dir(ex)
		return pwd
	}
	d, err := os.Getwd()
	if err != nil {
		d = "."
	}
	return d
}

func InitConfig() {
	pwd := PWD()
	dataDir := flags.DataDir
	if !filepath.IsAbs(dataDir) {
		flags.DataDir = filepath.Join(pwd, flags.DataDir)
	}
	// Determine config file path: use flags.ConfigPath if provided, otherwise default to <dataDir>/config.json
	configPath := flags.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(flags.DataDir, "config.json")
	} else {
		// if relative, resolve relative to working directory
		if !filepath.IsAbs(configPath) {
			if absPath, err := filepath.Abs(configPath); err == nil {
				configPath = absPath
			} else {
				configPath = filepath.Join(pwd, configPath)
			}
		}
	}
	configPath = filepath.Clean(configPath)
	conf.ConfigPath = configPath
	log.Infof("reading config file: %s", configPath)
	if !utils.Exists(configPath) {
		log.Infof("config file not exists, creating default config file")
		_, err := utils.CreateNestedFile(configPath)
		if err != nil {
			log.Fatalf("failed to create config file: %+v", err)
		}
		conf.Conf = conf.DefaultConfig(dataDir)
		LastLaunchedVersion = conf.Version
		conf.Conf.LastLaunchedVersion = conf.Version
		if !utils.WriteJsonToFile(configPath, conf.Conf) {
			log.Fatalf("failed to create default config file")
		}
	} else {
		configBytes, err := os.ReadFile(configPath)
		if err != nil {
			log.Fatalf("reading config file error: %+v", err)
		}
		conf.Conf = conf.DefaultConfig(dataDir)
		err = utils.Json.Unmarshal(configBytes, conf.Conf)
		if err != nil {
			log.Fatalf("load config error: %+v", err)
		}
		LastLaunchedVersion = conf.Conf.LastLaunchedVersion
		if strings.HasPrefix(conf.Version, "v") || LastLaunchedVersion == "" {
			conf.Conf.LastLaunchedVersion = conf.Version
		}
		// update config.json struct
		confBody, err := utils.Json.MarshalIndent(conf.Conf, "", "  ")
		if err != nil {
			log.Fatalf("marshal config error: %+v", err)
		}
		err = os.WriteFile(configPath, confBody, 0o777)
		if err != nil {
			log.Fatalf("update config struct error: %+v", err)
		}
	}
	if !conf.Conf.Force {
		confFromEnv(conf.Conf)
	}

	applyMaxConcurrency(conf.Conf)
	applyMemoryConfig(conf.Conf)

	if len(conf.Conf.Log.Filter.Filters) == 0 {
		conf.Conf.Log.Filter.Enable = false
	}
	convertAbsPaths(conf.Conf, pwd)

	err := os.MkdirAll(conf.Conf.TempDir, 0o777)
	if err != nil {
		log.Fatalf("create temp dir error: %+v", err)
	}
	log.Debugf("config: %+v", conf.Conf)

	// Validate and display proxy configuration status
	validateProxyConfig()

	base.InitClient()
	conf.URL = initURL(conf.Conf)
}

func confFromEnv(c *conf.Config) {
	prefix := "OPENLIST_"
	if flags.NoPrefix {
		prefix = ""
	}
	log.Infof("load config from env with prefix: %s", prefix)
	if err := env.ParseWithOptions(c, env.Options{
		Prefix: prefix,
	}); err != nil {
		log.Fatalf("load config from env error: %+v", err)
	}
}

func initURL(c *conf.Config) *url.URL {
	if !strings.Contains(c.SiteURL, "://") {
		c.SiteURL = utils.FixAndCleanPath(c.SiteURL)
	}
	u, err := url.Parse(c.SiteURL)
	if err != nil {
		utils.Log.Fatalf("can't parse site_url: %+v", err)
	}
	return u
}

func applyMaxConcurrency(c *conf.Config) {
	if c.MaxConcurrency > math.MaxInt32 {
		net.DefaultConcurrencyLimit = &net.ConcurrencyLimit{Limit: math.MaxInt32}
	} else if c.MaxConcurrency > 0 {
		net.DefaultConcurrencyLimit = &net.ConcurrencyLimit{Limit: uint32(c.MaxConcurrency)}
	}
}

func applyMemoryConfig(c *conf.Config) {
	memStat, _ := mem.VirtualMemory()
	if memStat != nil {
		log.Infof("total memory: %dMB, available: %dMB", memStat.Total>>20, memStat.Available>>20)
		if c.MinFreeMemory < 0 {
			conf.MinFreeMemory = 0
			log.Info("disable memory cache")
		} else {
			if c.MinFreeMemory < 16 {
				t := (memStat.Total >> 20) / 10
				conf.MinFreeMemory = max(16, min(t, 1024)) << 20
			} else {
				conf.MinFreeMemory = uint64(c.MinFreeMemory) << 20
			}
			log.Infof("min free memory: %dMB", conf.MinFreeMemory>>20)
		}

		if c.MaxBlockLimit < 4 {
			t := (memStat.Total >> 20) * 3 / 100
			conf.MaxBlockLimit = max(4, min(uint64(t), 64)) << 20
		} else {
			conf.MaxBlockLimit = uint64(c.MaxBlockLimit) << 20
		}
		log.Infof("max block limit: %dMB", conf.MaxBlockLimit>>20)
	} else {
		conf.MinFreeMemory = 0
		log.Warn("failed to get memory info, disable memory cache")
	}

	if c.AutoMemoryLimit > 0 {
		conf.AutoMemoryLimit = uint64(c.AutoMemoryLimit) << 20
	} else {
		conf.AutoMemoryLimit = 0
	}
	log.Infof("auto memory limit: %dMB", conf.AutoMemoryLimit>>20)
}

func convertAbsPaths(c *conf.Config, pwd string) {
	convertAbsPath := func(path *string) {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Join(pwd, *path)
		}
	}
	convertAbsPath(&c.Database.DBFile)
	convertAbsPath(&c.Scheme.CertFile)
	convertAbsPath(&c.Scheme.KeyFile)
	convertAbsPath(&c.Scheme.UnixFile)
	convertAbsPath(&c.Log.Name)
	convertAbsPath(&c.TempDir)
	convertAbsPath(&c.BleveDir)
	convertAbsPath(&c.DistDir)
}

func CleanTempDir() {
	files, err := os.ReadDir(conf.Conf.TempDir)
	if err != nil {
		log.Errorln("failed list temp file: ", err)
	}
	for _, file := range files {
		if err := os.RemoveAll(filepath.Join(conf.Conf.TempDir, file.Name())); err != nil {
			log.Errorln("failed delete temp file: ", err)
		}
	}
}

// validateProxyConfig validates proxy configuration and displays status at startup
func validateProxyConfig() {
	if conf.Conf.ProxyAddress != "" {
		if _, err := url.Parse(conf.Conf.ProxyAddress); err == nil {
			log.Infof("Proxy enabled: %s", conf.Conf.ProxyAddress)
		} else {
			log.Errorf("Invalid proxy address format: %s, error: %v", conf.Conf.ProxyAddress, err)
		}
	}
}
