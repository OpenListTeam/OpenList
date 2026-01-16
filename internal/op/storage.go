package op

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/generic_sync"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// Although the driver type is stored,
// there is a storage in each driver,
// so it should actually be a storage, just wrapped by the driver
var storagesMap generic_sync.MapOf[string, driver.Driver]

func GetAllStorages() []driver.Driver {
	return storagesMap.Values()
}

func HasStorage(mountPath string) bool {
	return storagesMap.Has(utils.FixAndCleanPath(mountPath))
}

func GetStorageByMountPath(mountPath string) (driver.Driver, error) {
	mountPath = utils.FixAndCleanPath(mountPath)
	storageDriver, ok := storagesMap.Load(mountPath)
	if !ok {
		return nil, errors.Errorf("no mount path for an storage is: %s", mountPath)
	}
	return storageDriver, nil
}

// CreateStorage Save the storage to database so storage can get an id
// then instantiate corresponding driver and save it in memory
func CreateStorage(ctx context.Context, storage model.Storage) (uint, error) {
	storage.Modified = time.Now()
	storage.MountPath = utils.FixAndCleanPath(storage.MountPath)
	var err error
	// check driver first
	driverName := storage.Driver
	driverNew, err := GetDriver(driverName)
	if err != nil {
		return 0, errors.WithMessage(err, "failed get driver new")
	}
	storageDriver := driverNew()
	// insert storage to database
	err = db.CreateStorage(&storage)
	if err != nil {
		return storage.ID, errors.WithMessage(err, "failed create storage in database")
	}
	// already has an id
	err = initStorage(ctx, storage, storageDriver)
	go callStorageHooks("add", storageDriver)
	if err != nil {
		return storage.ID, errors.Wrap(err, "failed init storage but storage is already created")
	}
	log.Debugf("storage %+v is created", storageDriver)
	return storage.ID, nil
}

// LoadStorage load exist storage in db to memory
func LoadStorage(ctx context.Context, storage model.Storage) error {
	storage.MountPath = utils.FixAndCleanPath(storage.MountPath)
	// check driver first
	driverName := storage.Driver
	driverNew, err := GetDriver(driverName)
	if err != nil {
		return errors.WithMessage(err, "failed get driver new")
	}
	storageDriver := driverNew()

	err = initStorage(ctx, storage, storageDriver)
	go callStorageHooks("add", storageDriver)
	log.Debugf("storage %+v is created", storageDriver)
	return err
}

func getCurrentGoroutineStack() string {
	buf := make([]byte, 1<<16)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}

// initStorage initialize the driver and store to storagesMap
func initStorage(ctx context.Context, storage model.Storage, storageDriver driver.Driver) (err error) {
	storageDriver.SetStorage(storage)
	driverStorage := storageDriver.GetStorage()
	defer func() {
		if err := recover(); err != nil {
			errInfo := fmt.Sprintf("[panic] err: %v\nstack: %s\n", err, getCurrentGoroutineStack())
			log.Errorf("panic init storage: %s", errInfo)
			driverStorage.SetStatus(errInfo)
			MustSaveDriverStorage(storageDriver)
			storagesMap.Store(driverStorage.MountPath, storageDriver)
		}
	}()
	// Unmarshal Addition
	err = utils.Json.UnmarshalFromString(driverStorage.Addition, storageDriver.GetAddition())
	if err == nil {
		if ref, ok := storageDriver.(driver.Reference); ok {
			if strings.HasPrefix(driverStorage.Remark, "ref:/") {
				refMountPath := driverStorage.Remark
				i := strings.Index(refMountPath, "\n")
				if i > 0 {
					refMountPath = refMountPath[4:i]
				} else {
					refMountPath = refMountPath[4:]
				}
				var refStorage driver.Driver
				refStorage, err = GetStorageByMountPath(refMountPath)
				if err != nil {
					err = fmt.Errorf("ref: %w", err)
				} else {
					err = ref.InitReference(refStorage)
					if err != nil && errs.IsNotSupportError(err) {
						err = fmt.Errorf("ref: storage is not %s", storageDriver.Config().Name)
					}
				}
			}
		}
	}
	if err == nil {
		err = storageDriver.Init(ctx)
	}
	storagesMap.Store(driverStorage.MountPath, storageDriver)
	if err != nil {
		if IsUseOnlineAPI(storageDriver) {
			driverStorage.SetStatus(utils.SanitizeHTML(err.Error()))
		} else {
			driverStorage.SetStatus(err.Error())
		}
		err = errors.Wrap(err, "failed init storage")
	} else {
		driverStorage.SetStatus(WORK)
	}
	MustSaveDriverStorage(storageDriver)
	return err
}

func IsUseOnlineAPI(storageDriver driver.Driver) bool {
	v := reflect.ValueOf(storageDriver.GetAddition())
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return false
	}
	field_v := v.FieldByName("UseOnlineAPI")
	if !field_v.IsValid() {
		return false
	}
	if field_v.Kind() != reflect.Bool {
		return false
	}
	return field_v.Bool()
}

func EnableStorage(ctx context.Context, id uint) error {
	storage, err := db.GetStorageById(id)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	if !storage.Disabled {
		return errors.Errorf("this storage have enabled")
	}
	storage.Disabled = false
	err = db.UpdateStorage(storage)
	if err != nil {
		return errors.WithMessage(err, "failed update storage in db")
	}
	err = LoadStorage(ctx, *storage)
	if err != nil {
		return errors.WithMessage(err, "failed load storage")
	}
	return nil
}

func DisableStorage(ctx context.Context, id uint) error {
	storage, err := db.GetStorageById(id)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	if storage.Disabled {
		return errors.Errorf("this storage have disabled")
	}
	storageDriver, err := GetStorageByMountPath(storage.MountPath)
	if err != nil {
		return errors.WithMessage(err, "failed get storage driver")
	}
	// drop the storage in the driver
	if err := storageDriver.Drop(ctx); err != nil {
		return errors.Wrap(err, "failed drop storage")
	}
	// delete the storage in the memory
	storage.Disabled = true
	storage.SetStatus(DISABLED)
	err = db.UpdateStorage(storage)
	if err != nil {
		return errors.WithMessage(err, "failed update storage in db")
	}
	storagesMap.Delete(storage.MountPath)
	go callStorageHooks("del", storageDriver)
	return nil
}

// UpdateStorage update storage
// get old storage first
// drop the storage then reinitialize
func UpdateStorage(ctx context.Context, storage model.Storage) error {
	oldStorage, err := db.GetStorageById(storage.ID)
	if err != nil {
		return errors.WithMessage(err, "failed get old storage")
	}
	if oldStorage.Driver != storage.Driver {
		return errors.Errorf("driver cannot be changed")
	}
	storage.Modified = time.Now()
	storage.MountPath = utils.FixAndCleanPath(storage.MountPath)
	err = db.UpdateStorage(&storage)
	if err != nil {
		return errors.WithMessage(err, "failed update storage in database")
	}
	if storage.Disabled {
		return nil
	}
	storageDriver, err := GetStorageByMountPath(oldStorage.MountPath)
	if oldStorage.MountPath != storage.MountPath {
		// mount path renamed, need to drop the storage
		storagesMap.Delete(oldStorage.MountPath)
		Cache.DeleteDirectoryTree(storageDriver, "/")
		Cache.InvalidateStorageDetails(storageDriver)
	}
	if err != nil {
		return errors.WithMessage(err, "failed get storage driver")
	}
	err = storageDriver.Drop(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed drop storage")
	}

	err = initStorage(ctx, storage, storageDriver)
	go callStorageHooks("update", storageDriver)
	log.Debugf("storage %+v is update", storageDriver)
	return err
}

func DeleteStorageById(ctx context.Context, id uint) error {
	storage, err := db.GetStorageById(id)
	if err != nil {
		return errors.WithMessage(err, "failed get storage")
	}
	var dropErr error = nil
	if !storage.Disabled {
		storageDriver, err := GetStorageByMountPath(storage.MountPath)
		if err != nil {
			return errors.WithMessage(err, "failed get storage driver")
		}
		// drop the storage in the driver
		if err := storageDriver.Drop(ctx); err != nil {
			dropErr = errors.Wrapf(err, "failed drop storage")
		}
		// delete the storage in the memory
		storagesMap.Delete(storage.MountPath)
		Cache.DeleteDirectoryTree(storageDriver, "/")
		Cache.InvalidateStorageDetails(storageDriver)
		go callStorageHooks("del", storageDriver)
	}
	// delete the storage in the database
	if err := db.DeleteStorageById(id); err != nil {
		return errors.WithMessage(err, "failed delete storage in database")
	}
	return dropErr
}

// MustSaveDriverStorage call from specific driver
func MustSaveDriverStorage(driver driver.Driver) {
	err := saveDriverStorage(driver)
	if err != nil {
		log.Errorf("failed save driver storage: %s", err)
	}
}

func saveDriverStorage(driver driver.Driver) error {
	storage := driver.GetStorage()
	addition := driver.GetAddition()
	str, err := utils.Json.MarshalToString(addition)
	if err != nil {
		return errors.Wrap(err, "error while marshal addition")
	}
	storage.Addition = str
	err = db.UpdateStorage(storage)
	if err != nil {
		return errors.WithMessage(err, "failed update storage in database")
	}
	return nil
}

// getStoragesByPath get storage by longest match path, contains balance storage.
// for example, there is /a/b,/a/c,/a/d/e,/a/d/e.balance
// getStoragesByPath(/a/d/e/f) => /a/d/e,/a/d/e.balance
func getStoragesByPath(path string) []driver.Driver {
	storages := make([]driver.Driver, 0)
	curSlashCount := 0
	storagesMap.Range(func(mountPath string, value driver.Driver) bool {
		mountPath = utils.GetActualMountPath(mountPath)
		// is this path
		if utils.IsSubPath(mountPath, path) {
			slashCount := strings.Count(utils.PathAddSeparatorSuffix(mountPath), "/")
			// not the longest match
			if slashCount > curSlashCount {
				storages = storages[:0]
				curSlashCount = slashCount
			}
			if slashCount == curSlashCount {
				storages = append(storages, value)
			}
		}
		return true
	})
	// make sure the order is the same for same input
	sort.Slice(storages, func(i, j int) bool {
		return storages[i].GetStorage().MountPath < storages[j].GetStorage().MountPath
	})
	return storages
}

// GetStorageVirtualFilesByPath Obtain the virtual file generated by the storage according to the path
// for example, there are: /a/b,/a/c,/a/d/e,/a/b.balance1,/av
// GetStorageVirtualFilesByPath(/a) => b,c,d
func GetStorageVirtualFilesByPath(prefix string) []model.Obj {
	return getStorageVirtualFilesByPath(prefix, nil, "")
}

func GetStorageVirtualFilesWithDetailsByPath(ctx context.Context, prefix string, hideDetails, refresh bool, filterByName string) []model.Obj {
	if hideDetails {
		return getStorageVirtualFilesByPath(prefix, nil, filterByName)
	}
	return getStorageVirtualFilesByPath(prefix, func(d driver.Driver, obj model.Obj) model.Obj {
		if _, ok := obj.(*model.ObjStorageDetails); ok {
			return obj
		}
		ret := &model.ObjStorageDetails{
			Obj:            obj,
			StorageDetails: nil,
		}
		resultChan := make(chan *model.StorageDetails, 1)
		go func(dri driver.Driver) {
			details, err := GetStorageDetails(ctx, dri, refresh)
			if err != nil {
				if !errors.Is(err, errs.NotImplement) && !errors.Is(err, errs.StorageNotInit) {
					log.Errorf("failed get %s storage details: %+v", dri.GetStorage().MountPath, err)
				}
			}
			resultChan <- details
		}(d)
		select {
		case r := <-resultChan:
			ret.StorageDetails = r
		case <-time.After(time.Second):
		}
		return ret
	}, filterByName)
}

func getStorageVirtualFilesByPath(prefix string, rootCallback func(driver.Driver, model.Obj) model.Obj, filterByName string) []model.Obj {
	files := make([]model.Obj, 0)
	storages := storagesMap.Values()
	sort.Slice(storages, func(i, j int) bool {
		if storages[i].GetStorage().Order == storages[j].GetStorage().Order {
			return storages[i].GetStorage().MountPath < storages[j].GetStorage().MountPath
		}
		return storages[i].GetStorage().Order < storages[j].GetStorage().Order
	})

	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	set := make(map[string]int)
	var wg sync.WaitGroup
	for _, v := range storages {
		// Exclude prefix itself and non prefix
		p, found := strings.CutPrefix(utils.GetActualMountPath(v.GetStorage().MountPath), prefix)
		if !found || p == "" {
			continue
		}
		name, _, found := strings.Cut(p, "/")
		if filterByName != "" && name != filterByName {
			continue
		}

		if idx, ok := set[name]; ok {
			if !found {
				files[idx].(*model.Object).Mask = model.Locked | model.Virtual
				if rootCallback != nil {
					wg.Add(1)
					go func() {
						defer wg.Done()
						files[idx] = rootCallback(v, files[idx])
					}()
				}
			}
			continue
		}
		set[name] = len(files)
		obj := &model.Object{
			Name:     name,
			Modified: v.GetStorage().Modified,
			IsFolder: true,
		}
		if !found {
			idx := len(files)
			obj.Mask = model.Locked | model.Virtual
			files = append(files, obj)
			if rootCallback != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					files[idx] = rootCallback(v, files[idx])
				}()
			}
		} else {
			obj.Mask = model.ReadOnly | model.Virtual
			files = append(files, obj)
		}
	}
	if rootCallback != nil {
		wg.Wait()
	}
	return files
}

var balanceMap generic_sync.MapOf[string, int]

// GetBalancedStorage get storage by path
func GetBalancedStorage(path string) driver.Driver {
	path = utils.FixAndCleanPath(path)
	storages := getStoragesByPath(path)
	storageNum := len(storages)
	switch storageNum {
	case 0:
		return nil
	case 1:
		return storages[0]
	default:
		virtualPath := utils.GetActualMountPath(storages[0].GetStorage().MountPath)
		i, _ := balanceMap.LoadOrStore(virtualPath, 0)
		i = (i + 1) % storageNum
		balanceMap.Store(virtualPath, i)
		return storages[i]
	}
}

var detailsG singleflight.Group[*model.StorageDetails]

func GetStorageDetails(ctx context.Context, storage driver.Driver, refresh ...bool) (*model.StorageDetails, error) {
	if storage.Config().CheckStatus && storage.GetStorage().Status != WORK {
		return nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", storage.GetStorage().Status)
	}
	wd, ok := storage.(driver.WithDetails)
	if !ok {
		return nil, errs.NotImplement
	}
	if !utils.IsBool(refresh...) {
		if ret, ok := Cache.GetStorageDetails(storage); ok {
			return ret, nil
		}
	}
	details, err, _ := detailsG.Do(storage.GetStorage().MountPath, func() (*model.StorageDetails, error) {
		ret, err := wd.GetDetails(ctx)
		if err != nil {
			return nil, err
		}
		Cache.SetStorageDetails(storage, ret)
		return ret, nil
	})
	return details, err
}
