package dbengine

import (
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// CreateMysqlCon creates MySQL database connections
func CreateMysqlCon(dsn string, gormConfig *gorm.Config) (con model.Connection, err error) {
	var (
		db *gorm.DB
	)

	// Create MySQL database connection
	db, err = gorm.Open(mysql.Open(dsn), gormConfig)
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot create MySQL database connection: %w", err)
	}

	// Get underlying database connection for configuration
	sqlDB, err := db.DB()
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot access underlying MySQL database connection: %w", err)
	}

	// Apply connection pool parameters from configuration
	// If Pool is nil, fall back to internal defaults to keep previous behavior.
	if conf.Conf.Database.Pool == nil {
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(0)
	} else {
		pool := conf.Conf.Database.Pool
		sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
		sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(time.Duration(pool.ConnMaxLifetimeSeconds) * time.Second)
	}

	// For MySQL, both read and write connections point to the same database instance
	return model.Connection{
		Read:  db, // Read connection
		Write: db, // Write connection
	}, nil
}
