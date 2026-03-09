package db

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
)

func GetVirtualHostByDomain(domain string) (*model.VirtualHost, error) {
	var v model.VirtualHost
	if err := db.Where("domain = ?", domain).First(&v).Error; err != nil {
		return nil, errors.Wrapf(err, "failed to select virtual host")
	}
	return &v, nil
}

func GetVirtualHostById(id uint) (*model.VirtualHost, error) {
	var v model.VirtualHost
	if err := db.First(&v, id).Error; err != nil {
		return nil, errors.Wrapf(err, "failed get virtual host")
	}
	return &v, nil
}

func CreateVirtualHost(v *model.VirtualHost) error {
	return errors.WithStack(db.Create(v).Error)
}

func UpdateVirtualHost(v *model.VirtualHost) error {
	return errors.WithStack(db.Save(v).Error)
}

func GetVirtualHosts(pageIndex, pageSize int) (vhosts []model.VirtualHost, count int64, err error) {
	vhostDB := db.Model(&model.VirtualHost{})
	if err = vhostDB.Count(&count).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get virtual hosts count")
	}
	if err = vhostDB.Order(columnName("id")).Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&vhosts).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed find virtual hosts")
	}
	return vhosts, count, nil
}

func DeleteVirtualHostById(id uint) error {
	return errors.WithStack(db.Delete(&model.VirtualHost{}, id).Error)
}
