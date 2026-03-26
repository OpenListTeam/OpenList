package bootstrap

import (
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap/data"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/plugin"
	_ "github.com/OpenListTeam/OpenList/v4/internal/plugin/all"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/google/uuid"
)

func Init() {
	InitConfig()
	Log()
	InitDB()
	data.InitData()
	InitStreamLimit()
	InitIndex()
	InitUpgradePatch()
}

func Release() {
	db.Close()
}

var (
	running bool
)

// Called by OpenList-Mobile
func IsRunning(t string) bool {
	return plugin.IsRunning(t)
}

func Start() {
	if conf.Conf.DelayedStart != 0 {
		utils.Log.Infof("delayed start for %d seconds", conf.Conf.DelayedStart)
		time.Sleep(time.Duration(conf.Conf.DelayedStart) * time.Second)
	}
	InitOfflineDownloadTools()
	LoadStorages()
	InitTaskManager()
	plugin.StartAll()
	running = true
}

func Shutdown(timeout time.Duration) {
	utils.Log.Println("Shutdown server...")
	fs.ArchiveContentUploadTaskManager.RemoveAll()
	_ = timeout
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		plugin.StopAll()
	}()
	wg.Wait()
	utils.Log.Println("Server exit")
	running = false
}

type EndpointStartFailedHook func(string, string)

type EndpointShutdownHook func(string)

var (
	endpointStartFailedHooks map[string]EndpointStartFailedHook
	endpointShutdownHooks    map[string]EndpointShutdownHook
)

func RegisterEndpointStartFailedHook(hook EndpointStartFailedHook) string {
	id := uuid.NewString()
	endpointStartFailedHooks[id] = hook
	return id
}

func RemoveEndpointStartFailedHook(id string) {
	delete(endpointStartFailedHooks, id)
}

func RegisterEndpointShutdownHook(hook EndpointShutdownHook) string {
	id := uuid.NewString()
	endpointShutdownHooks[id] = hook
	return id
}

func RemoveEndpointShutdownHook(id string) {
	delete(endpointShutdownHooks, id)
}

func handleEndpointStartFailedHooks(t string, err error) {
	for _, hook := range endpointStartFailedHooks {
		hook(t, err.Error())
	}
}

func handleEndpointShutdownHooks(t string) {
	for _, hook := range endpointShutdownHooks {
		hook(t)
	}
}

func init() {
	endpointShutdownHooks = make(map[string]EndpointShutdownHook)
	endpointStartFailedHooks = make(map[string]EndpointStartFailedHook)
}
