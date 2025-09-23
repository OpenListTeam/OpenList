package dbengine

import (
	"fmt"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// CreatePostgresCon creates PostgreSQL database connections
func CreatePostgresCon(dsn string, gormConfig *gorm.Config) (con model.Connection, err error) {
	var (
		db *gorm.DB
	)

	// Create PostgreSQL database connection
	db, err = gorm.Open(postgres.Open(dsn), gormConfig)
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot create PostgreSQL database connection: %w", err)
	}

	// Get underlying database connection for configuration
	sqlDB, err := db.DB()
	if err != nil {
		return model.Connection{}, fmt.Errorf("cannot access underlying PostgreSQL database connection: %w", err)
	}

	// Set connection pool parameters
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(0)

	// For PostgreSQL, both read and write connections point to the same database instance
	return model.Connection{
		Read:  db, // Read connection
		Write: db, // Write connection
	}, nil
}
