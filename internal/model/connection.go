package model

import (
	"fmt"
	"gorm.io/gorm"
)

// A Connection struct abstracts the access to the underlying database connections
// It is used for reading from a database connection and writing to a database connection.
// The writing is always assumed to be in the context of a database transaction.
type Connection struct {
	// Read is a read connection used for fast access to the underlying database transaction
	Read *gorm.DB
	// Write is a write connection which is used primarily to write in particular to create a transaction connection
	Write *gorm.DB
}

// R returns a suitable connection. It is either read focused connection
// or a transaction.
// The func panics if no read connection is available
func (c Connection) R() *gorm.DB {
	if c.Read == nil {
		panic("no read database connection is available")
	}
	return c.Read
}

// W retrieves a write connection. If this is a transaction use the tx connection
// The func panics if no write connection is available
func (c Connection) W() *gorm.DB {
	if c.Write == nil {
		panic("no write database connection is available")
	}
	return c.Write
}

// Close closes the connection and cleans up resources.
// If twiceFlag = true, need to close both writeDB and readDB.
// If twiceFlag = false, only need to close readDB (when readDB = writeDB).
func (c Connection) Close(twiceFlag bool) error {
	var err error

	// Close readDB
	readDBRaw, readErr := c.Read.DB()
	if readErr != nil {
		err = fmt.Errorf("failed to get read db: %s", readErr.Error())
	} else {
		if closeErr := readDBRaw.Close(); closeErr != nil {
			err = fmt.Errorf("failed to close read db: %s", closeErr.Error())
		}
	}

	// Close writeDB if twiceFlag is true
	if twiceFlag {
		writeDBRaw, writeErr := c.Write.DB()
		if writeErr != nil {
			if err != nil {
				err = fmt.Errorf("%s; failed to get write db: %s", err.Error(), writeErr.Error())
			} else {
				err = fmt.Errorf("failed to get write db: %s", writeErr.Error())
			}
		} else {
			if closeErr := writeDBRaw.Close(); closeErr != nil {
				if err != nil {
					err = fmt.Errorf("%s; failed to close write db: %s", err.Error(), closeErr.Error())
				} else {
					err = fmt.Errorf("failed to close write db: %s", closeErr.Error())
				}
			}
		}
	}

	return err
}
