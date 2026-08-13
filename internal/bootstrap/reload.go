package bootstrap

import (
	"context"
	"os"
	"reflect"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/search"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/OpenListTeam/OpenList/v4/server/static"
	log "github.com/sirupsen/logrus"
)

// configWatcher watches config.json for changes and triggers reloadConfig.
var configWatcher *conf.ConfigWatcher

// reloadConfig rebuilds the configuration from the provided raw config.json
// bytes and atomically swaps conf.Conf / conf.URL, then applies only the
// side effects for fields that actually changed. It never mutates the live
// conf.Conf in place: a brand-new *conf.Config is built offline and swapped
// in as a whole, so readers always see a consistent (old or new) config.
//
// On parse failure it returns a non-nil error and leaves the running config
// untouched, so the watcher retries on the next file event.
func reloadConfig(data []byte) error {
	pwd := PWD()
	dataDir := flags.DataDir

	newConf := conf.DefaultConfig(dataDir)
	if err := utils.Json.Unmarshal(data, newConf); err != nil {
		return err
	}

	// LastLaunchedVersion is a one-shot startup field; preserve the running
	// value so reload does not re-trigger upgrade patches or produce spurious diffs.
	newConf.LastLaunchedVersion = conf.Conf.LastLaunchedVersion

	if !newConf.Force {
		confFromEnv(newConf)
	}

	// Mirror the startup sanity check: an empty filter list disables filtering.
	if len(newConf.Log.Filter.Filters) == 0 {
		newConf.Log.Filter.Enable = false
	}

	convertAbsPaths(newConf, pwd)

	// Ensure the (possibly new) temp dir exists. Non-fatal on reload.
	if err := os.MkdirAll(newConf.TempDir, 0o777); err != nil {
		log.Warnf("create temp dir on reload error: %+v", err)
	}

	// Derive conf.URL from the new SiteURL offline (also fixes newConf.SiteURL).
	newURL := initURL(newConf)

	// Snapshot the old config, then swap the pointer and URL as a whole.
	oldConf := conf.Conf
	conf.Conf = newConf
	conf.URL = newURL

	applyReloadSideEffects(oldConf, newConf)

	log.Info("config reloaded successfully")
	return nil
}

// applyReloadSideEffects applies runtime side effects only for config fields
// that changed between oldConf and newConf. Unchanged subsystems are left
// untouched to minimise disruption.
func applyReloadSideEffects(oldConf, newConf *conf.Config) {
	// --- Log output configuration (lumberjack rotation / file) ---
	logOutputChanged := oldConf.Log.Enable != newConf.Log.Enable ||
		oldConf.Log.Name != newConf.Log.Name ||
		oldConf.Log.MaxSize != newConf.Log.MaxSize ||
		oldConf.Log.MaxBackups != newConf.Log.MaxBackups ||
		oldConf.Log.MaxAge != newConf.Log.MaxAge ||
		oldConf.Log.Compress != newConf.Log.Compress
	if logOutputChanged {
		Log()
		log.Info("log output config reloaded")
	}

	// --- Log filter rules ---
	if !reflect.DeepEqual(oldConf.Log.Filter, newConf.Log.Filter) {
		middlewares.ReloadFilterList()
		log.Info("log filter list reloaded")
	}

	// --- TLS verification / proxy (global HTTP clients) ---
	if oldConf.TlsInsecureSkipVerify != newConf.TlsInsecureSkipVerify ||
		oldConf.ProxyAddress != newConf.ProxyAddress {
		base.InitClient()
		validateProxyConfig()
		log.Info("http clients reloaded")
	}

	// --- JWT secret (invalidates already-issued tokens) ---
	if oldConf.JwtSecret != newConf.JwtSecret {
		common.SecretKey = []byte(newConf.JwtSecret)
		log.Info("jwt secret reloaded; previously issued tokens are now invalid")
	}

	// --- Site URL (conf.URL already updated above) ---
	if oldConf.SiteURL != newConf.SiteURL {
		log.Infof("site_url reloaded: %s", newConf.SiteURL)
	}

	// --- Max concurrency ---
	if oldConf.MaxConcurrency != newConf.MaxConcurrency {
		applyMaxConcurrency(newConf)
		log.Infof("max concurrency reloaded: %d", newConf.MaxConcurrency)
	}

	// --- Memory thresholds ---
	if oldConf.MinFreeMemory != newConf.MinFreeMemory ||
		oldConf.MaxBlockLimit != newConf.MaxBlockLimit ||
		oldConf.AutoMemoryLimit != newConf.AutoMemoryLimit {
		applyMemoryConfig(newConf)
		log.Info("memory limits reloaded")
	}

	// --- Group A: listener restart (scheme.* / s3.* / ftp.* / sftp.*) ---
	applyEndpointChanges(oldConf, newConf)

	// --- Group C: CORS live reload ---
	if !reflect.DeepEqual(oldConf.Cors, newConf.Cors) {
		server.ReloadCors()
		log.Info("cors config reloaded")
	}

	// --- Group D: subsystem re-init ---
	if !reflect.DeepEqual(oldConf.Database, newConf.Database) {
		log.Warn("database config changed; reinitializing (HIGH RISK: in-flight queries on old handle may fail)")
		if err := db.Reinit(newConf); err != nil {
			log.Errorf("database reinit failed: %v", err)
		} else {
			log.Info("database reinitialized successfully; old handle closes after 5 min")
		}
	}
	if oldConf.BleveDir != newConf.BleveDir || !reflect.DeepEqual(oldConf.Meilisearch, newConf.Meilisearch) {
		log.Info("search config changed; reinitializing search index")
		if oldConf.BleveDir != newConf.BleveDir {
			log.Warn("bleve_dir changed; index content will be empty — admin must rebuild via rescan")
		}
		if err := search.Reinit(); err != nil {
			log.Errorf("search reinit failed: %v", err)
		}
	}
	if oldConf.DistDir != newConf.DistDir || oldConf.Cdn != newConf.Cdn {
		static.Reload()
		log.Info("static resources (dist_dir/cdn) reloaded")
	}

	// --- Group E: no-op fields (take effect at next start) ---
	if oldConf.DelayedStart != newConf.DelayedStart {
		log.Info("delayed_start changed; takes effect at next start")
	}
	if oldConf.Force != newConf.Force {
		log.Info("force changed; takes effect at next start")
	}
	if oldConf.LastLaunchedVersion != newConf.LastLaunchedVersion {
		log.Info("last_launched_version changed; takes effect at next start")
	}
}

// startConfigWatcher starts the config.json hot-reload watcher.
func startConfigWatcher() {
	if conf.ConfigPath == "" {
		log.Warn("config path is empty, skipping config file watcher")
		return
	}
	w := conf.NewConfigWatcher(conf.ConfigPath, reloadConfig)
	if err := w.Start(context.Background()); err != nil {
		log.Errorf("failed to start config file watcher: %v", err)
		return
	}
	configWatcher = w
	log.Infof("config file watcher started: %s", conf.ConfigPath)
}

// stopConfigWatcher stops the config.json hot-reload watcher. Idempotent.
func stopConfigWatcher() {
	if configWatcher != nil {
		configWatcher.Stop()
		configWatcher = nil
	}
}
