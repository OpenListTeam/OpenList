package op

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// s3EtagModeDefaultMarker records that the shipped default of s3_etag_mode was
// already moved from "content" to "version".
const s3EtagModeDefaultMarker = "s3_etag_mode_default_marker"

// MigrateS3EtagModeDefault moves installs that still carry the original
// "content" default onto the "version" default. It only runs once: afterwards
// the marker is set, so an administrator who deliberately selects "content"
// keeps that choice across restarts.
func MigrateS3EtagModeDefault() {
	if marker, err := GetSettingItemByKey(s3EtagModeDefaultMarker); err == nil && marker.Value != "" {
		if marker.Group == model.S3 {
			marker.Group = -1
			marker.Flag = model.DEPRECATED
			_ = SaveSettingItem(marker)
		}
		return
	}
	if current, err := GetSettingItemByKey(conf.S3EtagMode); err == nil &&
		strings.EqualFold(strings.TrimSpace(current.Value), "content") {
		current.Value = "version"
		if err := SaveSettingItem(current); err != nil {
			utils.Log.Errorf("failed to move s3_etag_mode to the new default: %+v", err)
			return
		}
	}
	// Not fatal: without the marker the migration simply runs again on the next
	// start, which is what we want when the database is transiently unhappy.
	// Group is set to -1 with Flag DEPRECATED so it never leaks into visible setting groups.
	if err := SaveSettingItem(&model.SettingItem{
		Key:   s3EtagModeDefaultMarker,
		Value: "1",
		Type:  conf.TypeString,
		Group: -1,
		Flag:  model.DEPRECATED,
	}); err != nil {
		utils.Log.Errorf("failed to record the s3_etag_mode migration marker: %+v", err)
	}
}
