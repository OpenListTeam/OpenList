package handles

import (
	"sort"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap/data"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/common"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/OpenListTeam/OpenList/v4/plugins/server/static"
	"github.com/gin-gonic/gin"
)

func ResetToken(c *gin.Context) {
	token := random.Token()
	item := model.SettingItem{Key: "token", Value: token, Type: conf.TypeString, Group: model.SINGLE, Flag: model.PRIVATE}
	if err := op.SaveSettingItem(&item); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	sign.Instance()
	common.SuccessResp(c, token)
}

func GetSetting(c *gin.Context) {
	key := c.Query("key")
	keys := c.Query("keys")
	if key != "" {
		item, err := op.GetSettingItemByKey(key)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		common.SuccessResp(c, item)
	} else {
		items, err := op.GetSettingItemInKeys(strings.Split(keys, ","))
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		common.SuccessResp(c, items)
	}
}

func SaveSettings(c *gin.Context) {
	var req []model.SettingItem
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.SaveSettingItems(req); err != nil {
		common.ErrorResp(c, err, 500)
	} else {
		common.SuccessResp(c)
		static.UpdateIndex()
	}
}

func ListSettings(c *gin.Context) {
	groupStr := c.Query("group")
	groupsStr := c.Query("groups")
	var settings []model.SettingItem
	var err error
	if groupsStr == "" && groupStr == "" {
		settings, err = op.GetSettingItems()
	} else {
		var groupStrings []string
		if groupsStr != "" {
			groupStrings = strings.Split(groupsStr, ",")
		} else {
			groupStrings = append(groupStrings, groupStr)
		}
		var groups []int
		for _, str := range groupStrings {
			group, err := strconv.Atoi(str)
			if err != nil {
				common.ErrorResp(c, err, 400)
				return
			}
			groups = append(groups, group)
		}
		settings, err = op.GetSettingItemsInGroups(groups)
	}
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, settings)
}

func DefaultSettings(c *gin.Context) {
	groupStr := c.Query("group")
	groupsStr := c.Query("groups")
	settings := data.InitialSettings()
	if groupsStr == "" && groupStr == "" {
		for i := range settings {
			(&settings[i]).Index = uint(i)
		}
		common.SuccessResp(c, settings)
	} else {
		var groupStrings []string
		if groupsStr != "" {
			groupStrings = strings.Split(groupsStr, ",")
		} else {
			groupStrings = append(groupStrings, groupStr)
		}
		var groups []int
		for _, str := range groupStrings {
			group, err := strconv.Atoi(str)
			if err != nil {
				common.ErrorResp(c, err, 400)
				return
			}
			groups = append(groups, group)
		}
		sort.Ints(groups)
		var resultItems []model.SettingItem
		for _, group := range groups {
			for i := range settings {
				item := settings[i]
				if group == item.Group {
					item.Index = uint(i)
					resultItems = append(resultItems, item)
				}
			}
		}
		common.SuccessResp(c, resultItems)
	}
}

func DeleteSetting(c *gin.Context) {
	key := c.Query("key")
	if err := op.DeleteSettingItemByKey(key); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

func PublicSettings(c *gin.Context) {
	common.SuccessResp(c, op.GetPublicSettingsMap())
}
