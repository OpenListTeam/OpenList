package data

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

func initSettings() {
	initialSettingItems := InitialSettings()
	isActive := func(key string) bool {
		for _, item := range initialSettingItems {
			if item.Key == key {
				return true
			}
		}
		return false
	}
	// check deprecated
	settings, err := op.GetSettingItems()
	if err != nil {
		utils.Log.Fatalf("failed get settings: %+v", err)
	}
	settingMap := map[string]*model.SettingItem{}
	for _, v := range settings {
		if v.Key == "" {
			err := db.DeleteSettingItemByKey(v.Key)
			if err != nil {
				utils.Log.Errorf("failed delete setting with empty key: %+v", err)
			}
			continue
		}
		if !isActive(v.Key) && v.Flag != model.DEPRECATED {
			v.Flag = model.DEPRECATED
			err = op.SaveSettingItem(&v)
			if err != nil {
				utils.Log.Fatalf("failed save setting: %+v", err)
			}
		}
		settingMap[v.Key] = &v
	}
	op.MigrationSettingItems = map[string]op.MigrationValueItem{}
	// create or save setting
	var saveItems []model.SettingItem
	for i := range initialSettingItems {
		item := &initialSettingItems[i]
		item.Index = uint(i)
		migrationValue := item.MigrationValue
		if len(migrationValue) > 0 {
			op.MigrationSettingItems[item.Key] = op.MigrationValueItem{MigrationValue: item.MigrationValue, Value: item.Value}
			item.MigrationValue = ""
		}
		// err
		stored, ok := settingMap[item.Key]
		if !ok {
			stored, err = op.GetSettingItemByKey(item.Key)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				utils.Log.Fatalf("failed get setting: %+v", err)
				continue
			}
		}
		if item.Key != conf.VERSION && stored != nil &&
			(len(migrationValue) == 0 || stored.Value != migrationValue) {
			item.Value = stored.Value
		}
		_, err = op.HandleSettingItemHook(item)
		if err != nil {
			utils.Log.Errorf("failed to execute hook on %s: %+v", item.Key, err)
			continue
		}
		if stored == nil || *item != *stored {
			saveItems = append(saveItems, *item)
		}
	}
	if len(saveItems) > 0 {
		err = db.SaveSettingItems(saveItems)
		if err != nil {
			utils.Log.Fatalf("failed save setting: %+v", err)
		} else {
			op.SettingCacheUpdate()
		}
	}
}

func InitialSettings() []model.SettingItem {
	var token string
	if flags.Dev {
		token = "dev_token"
	} else {
		token = random.Token()
	}
	siteVersion := fmt.Sprintf("%s (Commit: %s) - Frontend: %s - Build at: %s", conf.Version, conf.GitCommit, conf.WebVersion, conf.BuiltAt)
	initialSettingItems := []model.SettingItem{
		// site settings
		{Key: conf.VERSION, Value: siteVersion, Type: conf.TypeString, Group: model.SITE, Flag: model.READONLY},
		//{Key: conf.ApiUrl, Value: "", Type: conf.TypeString, Group: model.SITE},
		//{Key: conf.BasePath, Value: "", Type: conf.TypeString, Group: model.SITE},
		{Key: conf.SiteTitle, Value: "OpenList", Type: conf.TypeString, Group: model.SITE},
		{Key: conf.Announcement, Value: "Welcome to the OpenList project!\nFor the latest updates, to contribute code, or to submit suggestions and issues, please visit our [project repository](https://github.com/OpenListTeam/OpenList).", Type: conf.TypeText, Group: model.SITE},
		{Key: "pagination_type", Value: "all", Type: conf.TypeSelect, Options: "all,pagination,load_more,auto_load_more", Group: model.SITE},
		{Key: "default_page_size", Value: "30", Type: conf.TypeNumber, Group: model.SITE},
		{Key: conf.AllowIndexed, Value: "false", Type: conf.TypeBool, Group: model.SITE},
		{Key: conf.AllowMounted, Value: "true", Type: conf.TypeBool, Group: model.SITE},
		{Key: conf.RobotsTxt, Value: "User-agent: *\nAllow: /", Type: conf.TypeText, Group: model.SITE},
		// style settings
		{Key: conf.Logo, Value: "https://res.oplist.org/logo/logo.svg", MigrationValue: "https://cdn.oplist.org/gh/OpenListTeam/Logo@main/logo.svg", Type: conf.TypeText, Group: model.STYLE},
		{Key: conf.Favicon, Value: "https://res.oplist.org/logo/logo.svg", MigrationValue: "https://cdn.oplist.org/gh/OpenListTeam/Logo@main/logo.svg", Type: conf.TypeString, Group: model.STYLE},
		{Key: conf.MainColor, Value: "#1890ff", Type: conf.TypeString, Group: model.STYLE},
		{Key: "home_icon", Value: "🏠", Type: conf.TypeString, Group: model.STYLE},
		{Key: "share_icon", Value: "🎁", Type: conf.TypeString, Group: model.STYLE},
		{Key: "home_container", Value: "max_980px", Type: conf.TypeSelect, Options: "max_980px,hope_container", Group: model.STYLE},
		{Key: "settings_layout", Value: "list", Type: conf.TypeSelect, Options: "list,responsive", Group: model.STYLE},
		{Key: conf.HideStorageDetails, Value: "true", Type: conf.TypeBool, Group: model.STYLE, Flag: model.PRIVATE},
		{Key: conf.HideStorageDetailsInManagePage, Value: "true", Type: conf.TypeBool, Group: model.STYLE, Flag: model.PRIVATE},
		{Key: "show_disk_usage_in_plain_text", Value: "false", Type: conf.TypeBool, Group: model.STYLE, Flag: model.PUBLIC},
		// preview settings
		{Key: conf.TextTypes, Value: "txt,htm,html,xml,java,properties,sql,js,md,json,conf,ini,vue,php,py,bat,gitignore,yml,go,sh,c,cpp,h,hpp,tsx,vtt,srt,ass,rs,lrc,strm", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		{Key: conf.AudioTypes, Value: "mp3,flac,ogg,m4a,wav,opus,wma", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		{Key: conf.VideoTypes, Value: "mp4,mkv,avi,mov,rmvb,webm,flv,m3u8", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		{Key: conf.ImageTypes, Value: "jpg,tiff,jpeg,png,gif,bmp,svg,ico,swf,webp,avif", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		//{Key: conf.OfficeTypes, Value: "doc,docx,xls,xlsx,ppt,pptx", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		{Key: conf.ProxyTypes, Value: "m3u8,url", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		{Key: conf.ProxyIgnoreHeaders, Value: "authorization,referer", Type: conf.TypeText, Group: model.PREVIEW, Flag: model.PRIVATE},
		{Key: "external_previews", Value: `{}`, Type: conf.TypeText, Group: model.PREVIEW},
		{Key: "iframe_previews", Value: `{
	"doc,docx,xls,xlsx,ppt,pptx": {
		"Microsoft":"https://view.officeapps.live.com/op/view.aspx?src=$e_url",
		"Google":"https://docs.google.com/gview?url=$e_url&embedded=true"
	},
	"pdf": {
		"PDF.js":"https://res.oplist.org/pdf.js/web/viewer.html?file=$e_url"
	},
	"epub": {
		"EPUB.js":"https://res.oplist.org/epub.js/viewer.html?url=$e_url"
	}
}`, Type: conf.TypeText, Group: model.PREVIEW},
		//		{Key: conf.OfficeViewers, Value: `{
		//	"Microsoft":"https://view.officeapps.live.com/op/view.aspx?src=$url",
		//	"Google":"https://docs.google.com/gview?url=$url&embedded=true",
		//}`, Type: conf.TypeText, Group: model.PREVIEW},
		//		{Key: conf.PdfViewers, Value: `{
		//	"pdf.js":"https://openlistteam.github.io/pdf.js/web/viewer.html?file=$url"
		//}`, Type: conf.TypeText, Group: model.PREVIEW},
		{Key: "audio_cover", Value: "https://res.oplist.org/logo/logo.svg", MigrationValue: "https://cdn.oplist.org/gh/OpenListTeam/Logo@main/logo.svg", Type: conf.TypeString, Group: model.PREVIEW},
		{Key: conf.AudioAutoplay, Value: "true", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.VideoAutoplay, Value: "true", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.PreviewDownloadByDefault, Value: "false", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.PreviewArchivesByDefault, Value: "true", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.SharePreviewDownloadByDefault, Value: "true", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.SharePreviewArchivesByDefault, Value: "false", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.ReadMeAutoRender, Value: "true", Type: conf.TypeBool, Group: model.PREVIEW},
		{Key: conf.FilterReadMeScripts, Value: "true", Type: conf.TypeBool, Group: model.PREVIEW}, // frontend
		{Key: conf.NonEFSZipEncoding, Value: "IBM437", Type: conf.TypeString, Group: model.PREVIEW},
		// global settings
		{Key: conf.HideFiles, Value: "/\\/README.md/i", Type: conf.TypeText, Group: model.GLOBAL},
		{Key: "package_download", Value: "true", Type: conf.TypeBool, Group: model.GLOBAL},
		{Key: conf.CustomizeHead, MigrationValue: `<script src="https://cdnjs.cloudflare.com/polyfill/v3/polyfill.min.js?features=String.prototype.replaceAll"></script>`, Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.CustomizeBody, Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.LinkExpiration, Value: "0", Type: conf.TypeNumber, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.SignAll, Value: "true", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PRIVATE},
		{
			Key: conf.PrivacyRegs, Value: `(?:(?:\d|[1-9]\d|1\d\d|2[0-4]\d|25[0-5])\.){3}(?:\d|[1-9]\d|1\d\d|2[0-4]\d|25[0-5])
([[:xdigit:]]{1,4}(?::[[:xdigit:]]{1,4}){7}|::|:(?::[[:xdigit:]]{1,4}){1,6}|[[:xdigit:]]{1,4}:(?::[[:xdigit:]]{1,4}){1,5}|(?:[[:xdigit:]]{1,4}:){2}(?::[[:xdigit:]]{1,4}){1,4}|(?:[[:xdigit:]]{1,4}:){3}(?::[[:xdigit:]]{1,4}){1,3}|(?:[[:xdigit:]]{1,4}:){4}(?::[[:xdigit:]]{1,4}){1,2}|(?:[[:xdigit:]]{1,4}:){5}:[[:xdigit:]]{1,4}|(?:[[:xdigit:]]{1,4}:){1,6}:)
(?U)access_token=(.*)&`,
			Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PRIVATE,
		},
		{Key: conf.OcrApi, Value: "https://openlistteam-ocr-api-server.hf.space/ocr/file/json", MigrationValue: "https://api.example.com/ocr/file/json", Type: conf.TypeString, Group: model.GLOBAL}, // TODO: This can be replace by a community-hosted endpoint, see https://github.com/OpenListTeam/ocr_api_server
		{Key: conf.FilenameCharMapping, Value: `{"/": "|"}`, Type: conf.TypeText, Group: model.GLOBAL},
		{Key: conf.ForwardDirectLinkParams, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL},
		{Key: conf.IgnoreDirectLinkParams, Value: "sign,openlist_ts,raw", Type: conf.TypeString, Group: model.GLOBAL},
		{Key: conf.WebauthnLoginEnabled, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PUBLIC},
		{Key: conf.SharePreview, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PUBLIC},
		{Key: conf.ShareArchivePreview, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PUBLIC},
		{Key: conf.ShareForceProxy, Value: "true", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.ShareSummaryContent, Value: "@{{creator}} shared {{#each files}}{{#if @first}}\"{{filename this}}\"{{/if}}{{#if @last}}{{#unless (eq @index 0)}} and {{@index}} more files{{/unless}}{{/if}}{{/each}} from {{site_title}}: {{base_url}}/@s/{{id}}{{#if pwd}} , the share code is {{pwd}}{{/if}}{{#if expires}}, please access before {{dateLocaleString expires}}.{{/if}}", Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PUBLIC},
		{Key: conf.HandleHookAfterWriting, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.HandleHookRateLimit, Value: "0", Type: conf.TypeNumber, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.IgnoreSystemFiles, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PRIVATE, Help: `When enabled, ignores common system files during upload (.DS_Store, desktop.ini, Thumbs.db, and files starting with ._)`},
		{Key: conf.SeedSiteURL, Value: "", Type: conf.TypeString, Group: model.GLOBAL, Flag: model.PRIVATE, Help: `Public base URL embedded in generated transfer seed sources when configured`},
		{Key: conf.SeedDefaultMatrix, Value: `{"md5":{"whole":true,"pieces":false},"sha1":{"whole":true,"pieces":false},"sha256":{"whole":true,"pieces":false}}`, Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PRIVATE, Help: `Default right-click hash matrix for transfer seed generation`},
		{Key: conf.SeedFormatPolicies, Value: `{"oss":"off","torrent":"off","cas":"off"}`, Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.SeedDefaultFormat, Value: "oss", Type: conf.TypeSelect, Options: "oss,torrent,cas", Group: model.GLOBAL, Flag: model.PRIVATE},
		{Key: conf.SeedSingleDirectPreview, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PUBLIC},
		{Key: conf.SeedCASDirectAccess, Value: "false", Type: conf.TypeBool, Group: model.GLOBAL, Flag: model.PUBLIC, Help: `When opening a single-file CAS seed, immediately rapid-upload it into the same folder and preview the restored file`},
		{Key: conf.SeedAutoGeneratePolicy, Value: "off", Type: conf.TypeSelect, Options: "off,on", Group: model.GLOBAL, Flag: model.PRIVATE, Help: `Global upload sidecar policy; storage-specific inheritance can be layered without changing the safe default`},
		{Key: conf.SeedDefaultTrackers, Value: "", Type: conf.TypeText, Group: model.GLOBAL, Flag: model.PRIVATE, Help: `Default tracker list offered when generating torrent seeds (one tracker per line)`},

		// single settings
		{Key: conf.Token, Value: token, Type: conf.TypeString, Group: model.SINGLE, Flag: model.PRIVATE},
		{Key: conf.SearchIndex, Value: "none", Type: conf.TypeSelect, Options: "database,database_non_full_text,bleve,meilisearch,none", Group: model.INDEX},
		{Key: conf.AutoUpdateIndex, Value: "false", Type: conf.TypeBool, Group: model.INDEX},
		{Key: conf.IgnorePaths, Value: "", Type: conf.TypeText, Group: model.INDEX, Flag: model.PRIVATE, Help: `one path per line`},
		{Key: conf.MaxIndexDepth, Value: "20", Type: conf.TypeNumber, Group: model.INDEX, Flag: model.PRIVATE, Help: `max depth of index`},
		{Key: conf.IndexProgress, Value: "{}", Type: conf.TypeText, Group: model.SINGLE, Flag: model.PRIVATE},

		// SSO settings
		{Key: conf.SSOLoginEnabled, Value: "false", Type: conf.TypeBool, Group: model.SSO, Flag: model.PUBLIC},
		{Key: conf.SSOLoginPlatform, Type: conf.TypeSelect, Options: "Casdoor,Github,Microsoft,Google,Dingtalk,OIDC", Group: model.SSO, Flag: model.PUBLIC},
		{Key: conf.SSOClientId, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOClientSecret, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOOIDCUsernameKey, Value: "name", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOOrganizationName, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOApplicationName, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOEndpointName, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOJwtPublicKey, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOExtraScopes, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOAutoRegister, Value: "false", Type: conf.TypeBool, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSODefaultDir, Value: "/", Type: conf.TypeString, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSODefaultPermission, Value: "0", Type: conf.TypeNumber, Group: model.SSO, Flag: model.PRIVATE},
		{Key: conf.SSOCompatibilityMode, Value: "false", Type: conf.TypeBool, Group: model.SSO, Flag: model.PUBLIC},
		{Key: conf.SSOPostMessageOrigin, Value: "", Type: conf.TypeString, Group: model.SSO, Flag: model.PUBLIC},

		// ldap settings
		{Key: conf.LdapLoginEnabled, Value: "false", Type: conf.TypeBool, Group: model.LDAP, Flag: model.PUBLIC},
		{Key: conf.LdapServer, Value: "", Type: conf.TypeString, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapSkipTlsVerify, Value: "false", Type: conf.TypeBool, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapManagerDN, Value: "", Type: conf.TypeString, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapManagerPassword, Value: "", Type: conf.TypeString, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapUserSearchBase, Value: "", Type: conf.TypeString, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapUserSearchFilter, Value: "(uid=%s)", Type: conf.TypeString, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapDefaultDir, Value: "/", Type: conf.TypeString, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapDefaultPermission, Value: "0", Type: conf.TypeNumber, Group: model.LDAP, Flag: model.PRIVATE},
		{Key: conf.LdapLoginTips, Value: "login with ldap", Type: conf.TypeString, Group: model.LDAP, Flag: model.PUBLIC},

		// s3 settings
		{Key: conf.S3AccessKeyId, Value: "", Type: conf.TypeString, Group: model.S3, Flag: model.PRIVATE},
		{Key: conf.S3SecretAccessKey, Value: "", Type: conf.TypeString, Group: model.S3, Flag: model.PRIVATE},
		{Key: conf.S3Buckets, Value: "[]", Type: conf.TypeString, Group: model.S3, Flag: model.PRIVATE},

		// ftp settings
		{Key: conf.FTPPublicHost, Value: "127.0.0.1", Type: conf.TypeString, Group: model.FTP, Flag: model.PRIVATE},
		{Key: conf.FTPPasvPortMap, Value: "", Type: conf.TypeText, Group: model.FTP, Flag: model.PRIVATE},
		{Key: conf.FTPMandatoryTLS, Value: "false", Type: conf.TypeBool, Group: model.FTP, Flag: model.PRIVATE},
		{Key: conf.FTPImplicitTLS, Value: "false", Type: conf.TypeBool, Group: model.FTP, Flag: model.PRIVATE},
		{Key: conf.FTPTLSPrivateKeyPath, Value: "", Type: conf.TypeString, Group: model.FTP, Flag: model.PRIVATE},
		{Key: conf.FTPTLSPublicCertPath, Value: "", Type: conf.TypeString, Group: model.FTP, Flag: model.PRIVATE},
		{Key: conf.SFTPDisablePasswordLogin, Value: "false", Type: conf.TypeBool, Group: model.FTP, Flag: model.PRIVATE},

		// traffic settings
		{Key: conf.TaskOfflineDownloadThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.Download.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.TaskOfflineDownloadTransferThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.Transfer.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.TaskUploadThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.Upload.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.TaskCopyThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.Copy.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.TaskMoveThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.Move.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.TaskDecompressDownloadThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.Decompress.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.TaskDecompressUploadThreadsNum, Value: strconv.Itoa(conf.Conf.Tasks.DecompressUpload.Workers), Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.StreamMaxClientDownloadSpeed, Value: "-1", Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.StreamMaxClientUploadSpeed, Value: "-1", Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.StreamMaxServerDownloadSpeed, Value: "-1", Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},
		{Key: conf.StreamMaxServerUploadSpeed, Value: "-1", Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PRIVATE},

		// media settings
		{Key: conf.MediaTMDBKey, Value: "", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaTMDBAPIURL, Value: "api.themoviedb.org", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaDiscogsToken, Value: "", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaDiscogsAPIURL, Value: "api.discogs.com", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaStoreThumbnail, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaThumbnailMode, Value: "runtime", Type: conf.TypeSelect, Options: "base64,runtime,local", Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `缩略图存储方式：base64 存入数据库；runtime 存入程序数据目录（data/imgs）；local 写入挂载目录下的 VFS 路径`},
		{Key: conf.MediaThumbnailPath, Value: "/imgs", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaScrapeConcurrency, Value: "5", Type: conf.TypeNumber, Group: model.MEDIA, Flag: model.PRIVATE},
		{Key: conf.MediaScrapeLanguage, Value: "zh-CN", Type: conf.TypeSelect, Options: "zh-CN,zh-TW,en-US,ja-JP,ko-KR,fr-FR,de-DE,es-ES,ru-RU,pt-BR", Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `首选刮削语言：影响 TMDB 的标题/简介/海报文字与 Google Books 的检索语言；豆瓣固定为中文`},
		{Key: conf.MediaBookSource, Value: "douban", Type: conf.TypeSelect, Options: "douban,google,openlibrary", Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `书籍刮削数据源：douban（豆瓣）、google（Google Books）、openlibrary（OpenLibrary）`},
		{Key: conf.MediaGoogleBooksKey, Value: "", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `Google Books API Key，可留空（无 Key 时配额较低）；申请地址 https://developers.google.com/books/docs/v1/using#APIKey`},
		{Key: conf.MediaDownloadCover, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `开启后把 TMDB / Google Books / OpenLibrary 等返回的封面链接下载到下方缩略图存储路径，而不是直接保存图片链接`},
		{Key: conf.MediaEnabled, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PUBLIC,
			Help: `媒体库总开关。关闭时媒体库相关页面、API 与协议端点全部不可用（返回 404），与未安装媒体库时表现一致`},
		{Key: conf.MediaEmbyEnabled, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `开启后提供 Emby 兼容 API（/@emby），可供 Infuse、VidHub、MoviePilot 等客户端直接接入`},
		{Key: conf.MediaSubsonicEnabled, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `开启后提供 OpenSubsonic API（/@rest），可供 Symfonium、Feishin、Amperfy 等音乐客户端接入`},
		{Key: conf.MediaSubsonicAllowPwd, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `允许 Subsonic 客户端用 OpenList 登录密码（u+p 参数）认证。该方式会把主账号密码暴露给第三方客户端，默认关闭，推荐使用应用密钥（apiKey 或 t+s）`},
		{Key: conf.MediaSubsonicProxyStream, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `Subsonic 播放默认返回 302 直链；若客户端不跟随重定向，可开启此项改为服务端代理转发（占用服务器带宽）`},
		{Key: conf.MediaProxyCover, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `开启后由服务端代理 TMDB/豆瓣等外部封面图片，适合直连图片域名受限的网络；默认关闭，直接使用原始图片地址`},
		{Key: conf.MediaCoverGateway, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `封面代理直连失败时，回退到第三方图片网关 wsrv.nl 重试。开启后封面地址会发送给该第三方服务；默认关闭`},
		{Key: conf.MediaStrmOnScan, Value: "false", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `扫描完成后自动把媒体条目生成为 .strm 文件并写入下方目录`},
		{Key: conf.MediaStrmOverwrite, Value: "true", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `生成 STRM 时覆盖已存在的同名文件`},
		{Key: conf.MediaStrmSameDir, Value: "true", Type: conf.TypeBool, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `开启时 .strm 与媒体文件放在同一目录且同名（仅扩展名不同）；关闭时统一输出到下方指定目录`},
		{Key: conf.MediaStrmPath, Value: "/strm", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `STRM 输出目录（VFS 路径，需位于可写存储中），仅在关闭「与媒体文件同目录」时使用`},
		{Key: conf.MediaStrmTemplate, Value: "{site_url}/d{path}?sign={sign}", Type: conf.TypeString, Group: model.MEDIA, Flag: model.PRIVATE,
			Help: `STRM 内容模板，可用变量：{site_url} 站点地址、{path} 文件虚拟路径、{sign} 签名、{name} 文件名`},

		// transcode settings (FFmpeg 云端/本地转码) - 默认全部关闭
		{Key: conf.TranscodeEnabled, Value: "false", Type: conf.TypeBool, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `开启后，超过阈值的媒体文件将通过 FFmpeg 转码后再播放；默认关闭`},
		{Key: conf.TranscodeRunMode, Value: "local", Type: conf.TypeSelect, Options: "local,remote,hybrid", Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `local=仅使用本机内置 worker；remote=只使用远程 Worker 节点；hybrid=本地优先，超载后派发到远程`},
		{Key: conf.TranscodeMinSizeGB, Value: "5", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `文件大于该 GB 数才走转码（小于则直链播放），0=任意大小都转码`},
		{Key: conf.TranscodeMinBitrateMbps, Value: "20", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `视频码率超过该 Mbps 才转码，0=不限制`},
		{Key: conf.TranscodeSourceCodecs, Value: "hevc,h265,av1,vvc,vp9", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `仅对这些源视频编码进行转码（逗号分隔）。常见高码率/兼容性差的编码：hevc,av1,vvc,vp9`},
		{Key: conf.TranscodeSourceExtensions, Value: "mkv,ts,m2ts,mov,avi,wmv,flv,rmvb,webm", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `仅对这些后缀进行转码（逗号分隔，不带点），mp4 默认不转码可直接播放`},
		{Key: conf.TranscodeOutputFormat, Value: "hls", Type: conf.TypeSelect, Options: "hls,dash,mp4", Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `输出封装格式，HLS 兼容性最好`},
		{Key: conf.TranscodeOutputCodec, Value: "h264", Type: conf.TypeSelect, Options: "h264,hevc,av1", Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `重新编码后的视频编码，推荐 h264（最广兼容、互联网分发免授权费）`},
		{Key: conf.TranscodeOutputBitrate, Value: "4000k", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `输出视频码率，例如 4000k / 6M。建议 1080p:4000k、720p:2500k`},
		{Key: conf.TranscodeOutputAudioCodec, Value: "aac", Type: conf.TypeSelect, Options: "aac,mp3,opus,copy", Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `输出音频编码，aac 兼容性最好；copy=直接复制源音频流`},
		{Key: conf.TranscodeOutputAudioBitrate, Value: "160k", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `输出音频码率`},
		{Key: conf.TranscodeOutputResolution, Value: "1920x1080", Type: conf.TypeSelect, Options: "source,3840x2160,2560x1440,1920x1080,1280x720,854x480", Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `输出分辨率上限，超过该分辨率会下采样；source=保持源分辨率`},
		{Key: conf.TranscodeSegmentDuration, Value: "6", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `HLS/DASH 切片时长(秒)，越小首帧越快但请求数变多，推荐 4-10`},
		{Key: conf.TranscodeHWAccel, Value: "none", Type: conf.TypeSelect,
			Options: "none,auto,nvenc,qsv,vaapi,amf,videotoolbox",
			Group:   model.TRANSCODE, Flag: model.PRIVATE,
			Help: `GPU 硬件加速：
none = 纯 CPU(libx264)
auto = 自动探测可用加速器
nvenc = NVIDIA GPU(GeForce/Tesla/Quadro/RTX,需 NVIDIA 驱动)
qsv = Intel 集显/独显 QuickSync(免费,功耗低)
vaapi = Linux 通用 VA-API(支持 Intel/AMD)
amf = AMD GPU(Windows AMF)
videotoolbox = macOS 硬件加速`},
		{Key: conf.TranscodeFFmpegPath, Value: "ffmpeg", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `FFmpeg 可执行路径，留空则使用 PATH 中的 ffmpeg`},
		{Key: conf.TranscodeFFprobePath, Value: "ffprobe", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `FFprobe 可执行路径`},
		{Key: conf.TranscodeWorkerSecret, Value: "", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `远程 Worker 注册时使用的共享密钥，留空则禁用远程 Worker 注册`},
		{Key: conf.TranscodeCachePath, Value: "data/ctmp", Type: conf.TypeString, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `转码切片缓存目录`},
		{Key: conf.TranscodeCacheMaxGB, Value: "20", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `切片缓存最大容量(GB)，超过后按 LRU 清理`},
		{Key: conf.TranscodeJobTimeoutMin, Value: "120", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `单个转码任务超时分钟数，超时自动失败`},
		{Key: conf.TranscodeLocalConcurrency, Value: "1", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `本机内置 worker 同时执行的转码任务数(local/hybrid 模式生效)`},
		{Key: conf.TranscodeIdleTimeoutSec, Value: "90", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `播放端无请求多少秒后自动停止转码（默认90秒，0=禁用）`},
		{Key: conf.TranscodeChunkDurationSec, Value: "60", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `每个 chunk 时长(秒)，越大每段缓冲越长但 ffmpeg 进程切换越少（默认60秒）`},
		{Key: conf.TranscodeMaxChunkConcurrency, Value: "2", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `单个视频任务最多同时运行的 chunk 数（默认2，越大并发越高但 CPU/GPU 占用越多）`},
		{Key: conf.TranscodeChunkIdleSec, Value: "60", Type: conf.TypeNumber, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `chunk 多久没被访问就 kill ffmpeg 释放资源（默认60秒）`},
		{Key: conf.TranscodeChunkPrefetch, Value: "true", Type: conf.TypeBool, Group: model.TRANSCODE, Flag: model.PRIVATE,
			Help: `当前 chunk 转码进度过半时，是否预启动下一个 chunk（默认开启，可让顺序播放无缝衔接）`},

		// multipart upload settings (from main)
		{Key: conf.MultipartEnabled, Value: "true", Type: conf.TypeBool, Group: model.TRAFFIC, Flag: model.PUBLIC},
		{Key: conf.MultipartChunkSize, Value: "10", Type: conf.TypeNumber, Group: model.TRAFFIC, Flag: model.PUBLIC, Help: `chunk size of multipart upload in MB (positive integer), keep it under your CDN's request body limit; each active session buffers up to 8 chunks on the server's disk`},
	}
	additionalSettingItems := tool.Tools.Items()
	// 固定顺序
	sort.Slice(additionalSettingItems, func(i, j int) bool {
		return additionalSettingItems[i].Key < additionalSettingItems[j].Key
	})
	initialSettingItems = append(initialSettingItems, additionalSettingItems...)
	if flags.Dev {
		initialSettingItems = append(initialSettingItems, []model.SettingItem{
			{Key: "test_deprecated", Value: "test_value", Type: conf.TypeString, Flag: model.DEPRECATED},
			{Key: "test_options", Value: "a", Type: conf.TypeSelect, Options: "a,b,c"},
			{Key: "test_help", Type: conf.TypeString, Help: "this is a help message"},
		}...)
	}
	return initialSettingItems
}
