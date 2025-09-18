package db

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
)

func GetMetaByPath(path string) (*model.Meta, error) {
	meta := model.Meta{Path: path}
	if err := rwDb.R().Where(meta).First(&meta).Error; err != nil {
		return nil, errors.Wrapf(err, "failed select meta")
	}
	return &meta, nil
}

func GetMetaById(id uint) (*model.Meta, error) {
	var u model.Meta
	if err := rwDb.R().First(&u, id).Error; err != nil {
		return nil, errors.Wrapf(err, "failed get old meta")
	}
	return &u, nil
}

func CreateMeta(u *model.Meta) error {
	return errors.WithStack(rwDb.W().Create(u).Error)
}

func UpdateMeta(u *model.Meta) error {
	return errors.WithStack(rwDb.W().Save(u).Error)
}

func GetMetas(pageIndex, pageSize int) (metas []model.Meta, count int64, err error) {
	metaDB := rwDb.W().Model(&model.Meta{})
	if err = metaDB.Count(&count).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get metas count")
	}
	if err = metaDB.Order(columnName("id")).Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&metas).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get find metas")
	}
	return metas, count, nil
}

func DeleteMetaById(id uint) error {
	return errors.WithStack(rwDb.W().Delete(&model.Meta{}, id).Error)
}
