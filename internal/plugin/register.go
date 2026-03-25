package plugin

import (
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	log "github.com/sirupsen/logrus"
)

var (
	plugins     map[string]Plugin
	initialized map[string]bool
	running     map[string]bool
	mu          sync.RWMutex
)

func RegisterPlugin(name string, construct func() Plugin) {
	if plugins == nil {
		plugins = make(map[string]Plugin)
	}
	if initialized == nil {
		initialized = make(map[string]bool)
	}
	if running == nil {
		running = make(map[string]bool)
	}
	mu.Lock()
	plugins[name] = construct()
	mu.Unlock()
}

func Init(name string, data map[string]any) error {
	mu.Lock()
	p, ok := plugins[name]
	if !ok {
		mu.Unlock()
		return nil
	}
	if initialized[name] {
		mu.Unlock()
		return nil
	}
	mu.Unlock()
	if err := p.Init(data); err != nil {
		return err
	}
	mu.Lock()
	initialized[name] = true
	mu.Unlock()
	return nil
}

func Start(name string) {
	mu.RLock()
	p := plugins[name]
	mu.RUnlock()
	if p == nil {
		log.Warnf("plugin %s is not registered", name)
		return
	}
	go func(name string, p Plugin) {
		setRunning(name, true)
		err := p.Start()
		setRunning(name, false)
		if err != nil {
			log.Errorf("start plugin %s failed: %+v", name, err)
		}
	}(name, p)

}

func Stop(name string) {
	mu.RLock()
	state, ok := running[name]
	p := plugins[name]
	mu.RUnlock()
	if !ok || !state || p == nil {
		return
	}
	if err := p.Stop(); err != nil {
		log.Errorf("stop plugin %s failed: %+v", name, err)
	}
	setRunning(name, false)
}

func StartAll() {
	for _, p := range conf.Conf.Plugins {
		if err := Init(p.Name, p.Data); err != nil {
			log.Errorf("init plugin %s failed: %+v", p.Name, err)
			continue
		}
		if p.Enable {
			Start(p.Name)
		}
	}
}

func StopAll() {
	mu.Lock()
	defer mu.Unlock()
	for name := range running {
		if state := running[name]; state {
			if err := plugins[name].Stop(); err != nil {
				log.Errorf("stop plugin %s failed: %+v", name, err)
			}
		}
		running[name] = false
	}
}

func IsRunning(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return running[name]
}

func setRunning(name string, value bool) {
	mu.Lock()
	defer mu.Unlock()
	running[name] = value
}
