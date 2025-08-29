package db

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model/tables"
	"github.com/pkg/errors"
)

func CreateSliceUpload(su *tables.SliceUpload) error {
	return errors.WithStack(db.Create(su).Error)
}

func GetSliceUpload(wh map[string]any) (*tables.SliceUpload, error) {
	su := &tables.SliceUpload{}
	return su, db.Where(wh).First(su).Error
}

func UpdateSliceUpload(su *tables.SliceUpload) error {
	return errors.WithStack(db.Save(su).Error)
}
