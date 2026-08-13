package db

import (
	"fmt"
	stdlog "log"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var (
	db   *gorm.DB
	dbMu sync.RWMutex
)

func Init(d *gorm.DB) {
	db = d
	err := AutoMigrate(new(model.Storage), new(model.User), new(model.Meta), new(model.SettingItem), new(model.SearchNode), new(model.TaskItem), new(model.SSHPublicKey), new(model.SharingDB))
	if err != nil {
		log.Fatalf("failed migrate database: %s", err.Error())
	}
}

func AutoMigrate(dst ...interface{}) error {
	var err error
	if conf.Conf.Database.Type == "mysql" {
		err = db.Set("gorm:table_options", "ENGINE=InnoDB CHARSET=utf8mb4").AutoMigrate(dst...)
	} else {
		err = db.AutoMigrate(dst...)
	}
	return err
}

// GetDb returns the current db handle. Callers that hold the handle briefly
// (single query) are safe; long-lived references should re-fetch via GetDb.
func GetDb() *gorm.DB {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return db
}

func Close() {
	log.Info("closing db")
	sqlDB, err := db.DB()
	if err != nil {
		log.Errorf("failed to get db: %s", err.Error())
		return
	}
	err = sqlDB.Close()
	if err != nil {
		log.Errorf("failed to close db: %s", err.Error())
		return
	}
}

// Reinit opens a new gorm.DB using the same construction logic as
// bootstrap.InitDB, runs AutoMigrate on it, then atomically swaps the
// package-level db handle under dbMu. The previous handle is closed after
// 5 minutes to allow in-flight queries to drain.
//
// This is HIGH RISK: in-flight queries that captured the old handle before
// the swap may fail when the old handle is eventually closed. Callers should
// prefer GetDb() per-query so they always observe the current handle.
func Reinit(newConf *conf.Config) error {
	newDB, err := openDB(newConf)
	if err != nil {
		return fmt.Errorf("open db on reinit failed: %w", err)
	}

	// Run AutoMigrate on the new DB before swapping it in so the new handle
	// is fully schema-consistent with the rest of the system.
	migrateModels := []interface{}{
		new(model.Storage), new(model.User), new(model.Meta), new(model.SettingItem),
		new(model.SearchNode), new(model.TaskItem), new(model.SSHPublicKey), new(model.SharingDB),
	}
	if newConf.Database.Type == "mysql" {
		err = newDB.Set("gorm:table_options", "ENGINE=InnoDB CHARSET=utf8mb4").AutoMigrate(migrateModels...)
	} else {
		err = newDB.AutoMigrate(migrateModels...)
	}
	if err != nil {
		// Close the new DB since we will not swap it in.
		if sqlDB, dbErr := newDB.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return fmt.Errorf("auto migrate on reinit failed: %w", err)
	}

	dbMu.Lock()
	oldDB := db
	db = newDB
	dbMu.Unlock()

	log.Warn("database reinitialized at runtime (HIGH RISK): in-flight queries on the old handle may fail; old handle closes after 5 min")

	// Schedule the old DB closure after 5 minutes to let in-flight queries drain.
	if oldDB != nil {
		time.AfterFunc(5*time.Minute, func() {
			sqlDB, err := oldDB.DB()
			if err != nil {
				log.Errorf("failed to get old db for deferred close: %s", err.Error())
				return
			}
			if err := sqlDB.Close(); err != nil {
				log.Errorf("failed to close old db: %s", err.Error())
				return
			}
			log.Info("old db handle closed after reinit grace period")
		})
	}

	return nil
}

// openDB constructs a *gorm.DB from the provided config using the same logic
// as bootstrap.InitDB: sqlite3/mysql/postgres driver selection, gorm logger,
// naming strategy, and DSN construction. Mirrors internal/bootstrap/db.go.
func openDB(newConf *conf.Config) (*gorm.DB, error) {
	logLevel := logger.Silent
	if flags.Debug || flags.Dev {
		logLevel = logger.Info
	}
	newLogger := logger.New(
		stdlog.New(log.StandardLogger().Out, "\r\n", stdlog.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
	gormConfig := &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix: newConf.Database.TablePrefix,
		},
		Logger: newLogger,
	}

	if flags.Dev {
		return gorm.Open(openSQLite("file::memory:?cache=shared"), gormConfig)
	}

	database := newConf.Database
	switch database.Type {
	case "sqlite3":
		{
			if !(strings.HasSuffix(database.DBFile, ".db") && len(database.DBFile) > 3) {
				return nil, fmt.Errorf("db name error: %s", database.DBFile)
			}
			return gorm.Open(openSQLite(fmt.Sprintf("%s?_journal=WAL&_vacuum=incremental",
				database.DBFile)), gormConfig)
		}
	case "mysql":
		{
			dsn := database.DSN
			if dsn == "" {
				//[username[:password]@][protocol[(address)]]/dbname[?param1=value1&...&paramN=valueN]
				dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&tls=%s",
					database.User, database.Password, database.Host, database.Port, database.Name, database.SSLMode)
			}
			return gorm.Open(mysql.Open(dsn), gormConfig)
		}
	case "postgres":
		{
			dsn := database.DSN
			if dsn == "" {
				if database.Password != "" {
					dsn = fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=Asia/Shanghai",
						database.Host, database.User, database.Password, database.Name, database.Port, database.SSLMode)
				} else {
					dsn = fmt.Sprintf("host=%s user=%s dbname=%s port=%d sslmode=%s TimeZone=Asia/Shanghai",
						database.Host, database.User, database.Name, database.Port, database.SSLMode)
				}
			}
			return gorm.Open(postgres.Open(dsn), gormConfig)
		}
	default:
		return nil, fmt.Errorf("not supported database type: %s", database.Type)
	}
}
