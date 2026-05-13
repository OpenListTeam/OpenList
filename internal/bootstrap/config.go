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
		confFromEnv()
	}

	if conf.Conf.MaxConcurrency > math.MaxInt32 {
		net.DefaultConcurrencyLimit = &net.ConcurrencyLimit{Limit: math.MaxInt32}
	} else if conf.Conf.MaxConcurrency > 0 {
		net.DefaultConcurrencyLimit = &net.ConcurrencyLimit{Limit: uint32(conf.Conf.MaxConcurrency)}
	}

	memStat, _ := mem.VirtualMemory()
	if memStat != nil {
		log.Infof("total memory: %dMB, available: %dMB", memStat.Total>>20, memStat.Available>>20)
		if conf.Conf.MinFreeMemory < 0 {
			conf.MinFreeMemory = 0
			log.Info("disable memory cache")
		} else {
			if conf.Conf.MinFreeMemory < 16 {
				t := (memStat.Total >> 20) / 10
				conf.MinFreeMemory = max(16, min(t, 1024)) << 20
			} else {
				conf.MinFreeMemory = uint64(conf.Conf.MinFreeMemory) << 20
			}
			log.Infof("min free memory: %dMB", conf.MinFreeMemory>>20)
		}

		if conf.Conf.MaxBlockLimit < 4 {
			t := (memStat.Total >> 20) * 3 / 100
			conf.MaxBlockLimit = max(4, min(uint64(t), 64)) << 20
		} else {
			conf.MaxBlockLimit = uint64(conf.Conf.MaxBlockLimit) << 20
		}
		log.Infof("max block limit: %dMB", conf.MaxBlockLimit>>20)
	} else {
		conf.MinFreeMemory = 0
		log.Warn("failed to get memory info, disable memory cache")
	}

	if conf.Conf.CacheThreshold > 0 {
		conf.CacheThreshold = uint64(conf.Conf.CacheThreshold) << 20
	} else {
		conf.CacheThreshold = 0
	}
	log.Infof("cache threshold: %dMB", conf.CacheThreshold>>20)

	if len(conf.Conf.Log.Filter.Filters) == 0 {
		conf.Conf.Log.Filter.Enable = false
	}
	// convert abs path
	convertAbsPath := func(path *string) {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Join(pwd, *path)
		}
	}
	convertAbsPath(&conf.Conf.Database.DBFile)
	convertAbsPath(&conf.Conf.Scheme.CertFile)
	convertAbsPath(&conf.Conf.Scheme.KeyFile)
	convertAbsPath(&conf.Conf.Scheme.UnixFile)
	convertAbsPath(&conf.Conf.Log.Name)
	convertAbsPath(&conf.Conf.TempDir)
	convertAbsPath(&conf.Conf.BleveDir)
	convertAbsPath(&conf.Conf.DistDir)

	err := os.MkdirAll(conf.Conf.TempDir, 0o777)
	if err != nil {
		log.Fatalf("create temp dir error: %+v", err)
	}
	log.Debugf("config: %+v", conf.Conf)

	// Validate and display proxy configuration status
	validateProxyConfig()

	base.InitClient()
	initURL()
}

func confFromEnv() {
	prefix := "OPENLIST_"
	if flags.NoPrefix {
		prefix = ""
	}
	log.Infof("load config from env with prefix: %s", prefix)
	if err := env.ParseWithOptions(conf.Conf, env.Options{
		Prefix: prefix,
	}); err != nil {
		log.Fatalf("load config from env error: %+v", err)
	}
}

func initURL() {
	if !strings.Contains(conf.Conf.SiteURL, "://") {
		conf.Conf.SiteURL = utils.FixAndCleanPath(conf.Conf.SiteURL)
	}
	u, err := url.Parse(conf.Conf.SiteURL)
	if err != nil {
		utils.Log.Fatalf("can't parse site_url: %+v", err)
	}
	conf.URL = u
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
