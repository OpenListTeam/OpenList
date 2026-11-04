package db

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
)

// migrateS3Markers upgrades the early key-only hashes before adding the unique
// bucket-aware hash index.
func migrateS3Markers() error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&model.S3Marker{}); err != nil {
		return err
	}
	table := stmt.Schema.Table
	m := db.Migrator()
	if !m.HasTable(table) {
		return nil
	}

	var rows []model.S3Marker
	if err := db.Model(&model.S3Marker{}).Select("id, bucket, key, key_hash").Order("id").Find(&rows).Error; err != nil {
		return err
	}
	identities := make(map[string]uint, len(rows))
	hashes := make(map[string]string, len(rows))
	for _, row := range rows {
		identity := row.Bucket + "\x00" + row.Key
		hash := model.HashS3MarkerKey(row.Bucket, row.Key)
		if previousIdentity, ok := hashes[hash]; ok && previousIdentity != identity {
			return fmt.Errorf("s3 marker hash collision between %q and %q", previousIdentity, identity)
		}
		hashes[hash] = identity
	}
	for _, row := range rows {
		identity := row.Bucket + "\x00" + row.Key
		if previousID, ok := identities[identity]; ok {
			if err := db.Delete(&model.S3Marker{}, previousID).Error; err != nil {
				return err
			}
		}
		identities[identity] = row.ID
		hash := model.HashS3MarkerKey(row.Bucket, row.Key)
		if row.KeyHash != hash {
			if err := db.Model(&model.S3Marker{}).Where("id = ?", row.ID).UpdateColumn("key_hash", hash).Error; err != nil {
				return err
			}
		}
	}

	const oldIndex = "idx_s3_marker_bucket_key_hash"
	if m.HasIndex(table, oldIndex) {
		return m.DropIndex(table, oldIndex)
	}
	return nil
}
