// watcher.go implements a config file hot-reload watcher for the conf package.
// It debounces noisy filesystem events, deduplicates reloads via SHA-256 content
// hashing, and handles the atomic-replace pattern (editor writes a temp file then
// renames it over the target) by re-adding the watch after a short settle delay.

package conf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
)

const (
	// configReloadDebounce coalesces rapid successive write events into a single reload.
	configReloadDebounce = 150 * time.Millisecond
	// replaceCheckDelay is a short settle window used after a Remove/Rename event to
	// decide whether the config file reappeared (atomic replace) or was truly deleted.
	replaceCheckDelay = 50 * time.Millisecond
)

// ConfigWatcher watches the OpenList config file and invokes the supplied
// reloadFn with the raw file bytes whenever the content actually changes.
// Reloads are debounced and de-duplicated by SHA-256 hash.
type ConfigWatcher struct {
	configPath  string
	reloadFn    func(data []byte) error
	watcher     *fsnotify.Watcher
	lastHash    string
	mu          sync.Mutex // guards lastHash and reloadTimer
	reloadTimer *time.Timer
	stopped     atomic.Bool
	stopOnce    sync.Once
	cancel      context.CancelFunc
}

// NewConfigWatcher creates a watcher for the given config file path.
// reloadFn is called with the raw file bytes when the file content actually
// changed (debounced + hash-checked). If reloadFn returns a non-nil error,
// the lastHash is NOT updated so the next event retries.
func NewConfigWatcher(configPath string, reloadFn func(data []byte) error) *ConfigWatcher {
	return &ConfigWatcher{
		configPath: configPath,
		reloadFn:   reloadFn,
	}
}

// Start begins watching the config file. It adds the file to fsnotify and
// spawns an event-loop goroutine that runs until ctx is cancelled or Stop is called.
// Start does NOT perform an initial reload (the config is already loaded at boot).
func (w *ConfigWatcher) Start(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = watcher

	if err := w.watcher.Add(w.configPath); err != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, err)
		_ = w.watcher.Close()
		w.watcher = nil
		return err
	}
	log.Debugf("watching config file: %s", w.configPath)

	// Derive a cancellable context so Stop can break the event loop goroutine.
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	go w.processEvents(ctx)
	return nil
}

// Stop stops the watcher. It is idempotent (safe to call multiple times).
func (w *ConfigWatcher) Stop() {
	w.stopOnce.Do(func() {
		w.stopped.Store(true)
		if w.cancel != nil {
			w.cancel()
		}
		w.mu.Lock()
		if w.reloadTimer != nil {
			w.reloadTimer.Stop()
			w.reloadTimer = nil
		}
		w.mu.Unlock()
		if w.watcher != nil {
			_ = w.watcher.Close()
		}
	})
}

// processEvents is the event-loop goroutine. It exits when ctx is cancelled,
// the watcher channels are closed, or the watcher has been stopped.
func (w *ConfigWatcher) processEvents(ctx context.Context) {
	for {
		if w.stopped.Load() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if w.stopped.Load() {
				return
			}
			w.handleEvent(event)
		case errWatch, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if w.stopped.Load() {
				return
			}
			log.Errorf("file watcher error: %v", errWatch)
		}
	}
}

// handleEvent filters fsnotify events down to those targeting the config file
// and dispatches Write/Create to a debounced reload, while Remove/Rename are
// treated as the atomic-replace case.
func (w *ConfigWatcher) handleEvent(event fsnotify.Event) {
	configOps := fsnotify.Write | fsnotify.Create | fsnotify.Rename | fsnotify.Remove
	if w.normalizePath(event.Name) != w.normalizePath(w.configPath) {
		return
	}
	if event.Op&configOps == 0 {
		return
	}

	log.Debugf("config file event detected: %s %s", event.Op.String(), event.Name)

	if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
		w.scheduleConfigReload()
		return
	}

	// Remove or Rename: atomic-replace case (editor writes temp file then renames
	// over the target). Wait briefly; if the file reappears, re-add the watch and
	// reload. If it is genuinely gone, warn and skip reloading.
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		go func() {
			time.Sleep(replaceCheckDelay)
			if w.stopped.Load() {
				return
			}
			if _, statErr := os.Stat(w.configPath); statErr == nil {
				if errAdd := w.watcher.Add(w.configPath); errAdd != nil {
					log.Debugf("failed to re-add config file watch: %v", errAdd)
				}
				w.scheduleConfigReload()
			} else {
				log.Warnf("config file disappeared, not reloading: %s", w.configPath)
			}
		}()
	}
}

// scheduleConfigReload debounces reload triggers: any pending reload is cancelled
// and replaced with a fresh timer firing after configReloadDebounce.
func (w *ConfigWatcher) scheduleConfigReload() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reloadTimer != nil {
		w.reloadTimer.Stop()
	}
	w.reloadTimer = time.AfterFunc(configReloadDebounce, w.reloadIfChanged)
}

// reloadIfChanged reads the config file, compares its SHA-256 hash against the
// last successfully reloaded content, and invokes reloadFn only on a real change.
// On reloadFn error the hash is left untouched so the next event retries.
func (w *ConfigWatcher) reloadIfChanged() {
	if w.stopped.Load() {
		return
	}
	data, err := os.ReadFile(w.configPath)
	if err != nil {
		log.Errorf("failed to read config file for reload: %v", err)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty config file write event")
		return
	}
	sum := sha256.Sum256(data)
	newHash := hex.EncodeToString(sum[:])

	w.mu.Lock()
	lastHash := w.lastHash
	w.mu.Unlock()

	if lastHash != "" && lastHash == newHash {
		log.Debugf("config file content unchanged (hash match), skipping reload")
		return
	}

	log.Infof("config file changed, reloading: %s", w.configPath)
	if err := w.reloadFn(data); err != nil {
		log.Errorf("failed to reload config: %v", err)
		return
	}

	w.mu.Lock()
	w.lastHash = newHash
	w.mu.Unlock()
}

// normalizePath cleans a path and strips Windows extended-length prefixes
// (\\?\ and \\?\UNC\), then lowercases it for case-insensitive comparison.
// Lowercasing is applied cross-platform; it is a no-op for the equality check
// on case-sensitive filesystems since both sides are normalized the same way.
func (w *ConfigWatcher) normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if strings.HasPrefix(cleaned, `\\?\UNC\`) {
		cleaned = `\\` + strings.TrimPrefix(cleaned, `\\?\UNC\`)
	} else {
		cleaned = strings.TrimPrefix(cleaned, `\\?\`)
	}
	return strings.ToLower(cleaned)
}
