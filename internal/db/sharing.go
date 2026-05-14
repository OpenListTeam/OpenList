package db

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/pkg/errors"
)

func GetSharingById(id string) (*model.SharingDB, error) {
	s := model.SharingDB{ID: id}
	if err := db.Where(s).First(&s).Error; err != nil {
		return nil, errors.Wrapf(err, "failed get sharing")
	}
	return &s, nil
}

// GetSharingByDomain 根据绑定的域名查询 sharing 记录（用于虚拟主机能力）。
// 仅当 sharing.Domain 字段精确匹配时返回；调用方需自行判断 Disabled / Expires / Files 等有效性。
func GetSharingByDomain(domain string) (*model.SharingDB, error) {
	var s model.SharingDB
	if err := db.Where("domain = ?", domain).First(&s).Error; err != nil {
		return nil, errors.Wrapf(err, "failed get sharing by domain")
	}
	return &s, nil
}

func GetSharings(pageIndex, pageSize int) (sharings []model.SharingDB, count int64, err error) {
	sharingDB := db.Model(&model.SharingDB{})
	if err := sharingDB.Count(&count).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get sharings count")
	}
	if err := sharingDB.Order(columnName("id")).Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&sharings).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get find sharings")
	}
	return sharings, count, nil
}

func GetSharingsByCreatorId(creator uint, pageIndex, pageSize int) (sharings []model.SharingDB, count int64, err error) {
	sharingDB := db.Model(&model.SharingDB{})
	cond := model.SharingDB{CreatorId: creator}
	if err := sharingDB.Where(cond).Count(&count).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get sharings count")
	}
	if err := sharingDB.Where(cond).Order(columnName("id")).Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&sharings).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get find sharings")
	}
	return sharings, count, nil
}

func CreateSharing(s *model.SharingDB) (string, error) {
	// domain 非空时做唯一性提前校验
	if s.Domain != "" {
		var exist model.SharingDB
		if err := db.Where("domain = ?", s.Domain).First(&exist).Error; err == nil {
			return "", errors.New("domain already used")
		}
	}
	if s.ID == "" {
		id := random.String(8)
		for len(id) < 12 {
			old := model.SharingDB{
				ID: id,
			}
			if err := db.Where(old).First(&old).Error; err != nil {
				s.ID = id
				return id, errors.WithStack(db.Create(s).Error)
			}
			id += random.String(1)
		}
		return "", errors.New("failed find valid id")
	} else {
		query := model.SharingDB{ID: s.ID}
		if err := db.Where(query).First(&query).Error; err == nil {
			return "", errors.New("sharing already exist")
		}
		return s.ID, errors.WithStack(db.Create(s).Error)
	}
}

func UpdateSharing(s *model.SharingDB) error {
	// domain 非空时校验唯一性（排除自身）
	if s.Domain != "" {
		var exist model.SharingDB
		if err := db.Where("domain = ? AND id <> ?", s.Domain, s.ID).First(&exist).Error; err == nil {
			return errors.New("domain already used")
		}
	}
	return errors.WithStack(db.Save(s).Error)
}

func DeleteSharingById(id string) error {
	s := model.SharingDB{ID: id}
	return errors.WithStack(db.Where(s).Delete(&s).Error)
}

func DeleteSharingsByCreatorId(creatorId uint) error {
	return errors.WithStack(db.Where("creator_id = ?", creatorId).Delete(&model.SharingDB{}).Error)
}
