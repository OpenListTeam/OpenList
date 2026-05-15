package conf

const (
	TypeString = "string"
	TypeSelect = "select"
	TypeBool   = "bool"
	TypeText   = "text"
	TypeNumber = "number"
)

const (
	// site
	VERSION      = "version"
	SiteTitle    = "site_title"
	Announcement = "announcement"
	AllowIndexed = "allow_indexed"
	AllowMounted = "allow_mounted"
	RobotsTxt    = "robots_txt"

	Logo                           = "logo" // multi-lines text, L1: light, EOL: dark
	Favicon                        = "favicon"
	MainColor                      = "main_color"
	HideStorageDetails             = "hide_storage_details"
	HideStorageDetailsInManagePage = "hide_storage_details_in_manage_page"

	// preview
	TextTypes                     = "text_types"
	AudioTypes                    = "audio_types"
	VideoTypes                    = "video_types"
	ImageTypes                    = "image_types"
	ProxyTypes                    = "proxy_types"
	ProxyIgnoreHeaders            = "proxy_ignore_headers"
	AudioAutoplay                 = "audio_autoplay"
	VideoAutoplay                 = "video_autoplay"
	PreviewDownloadByDefault      = "preview_download_by_default"
	PreviewArchivesByDefault      = "preview_archives_by_default"
	SharePreviewDownloadByDefault = "share_preview_download_by_default"
	SharePreviewArchivesByDefault = "share_preview_archives_by_default"
	ReadMeAutoRender              = "readme_autorender"
	FilterReadMeScripts           = "filter_readme_scripts"
	NonEFSZipEncoding             = "non_efs_zip_encoding"

	// global
	HideFiles               = "hide_files"
	CustomizeHead           = "customize_head"
	CustomizeBody           = "customize_body"
	LinkExpiration          = "link_expiration"
	SignAll                 = "sign_all"
	PrivacyRegs             = "privacy_regs"
	OcrApi                  = "ocr_api"
	FilenameCharMapping     = "filename_char_mapping"
	ForwardDirectLinkParams = "forward_direct_link_params"
	IgnoreDirectLinkParams  = "ignore_direct_link_params"
	WebauthnLoginEnabled    = "webauthn_login_enabled"
	SharePreview            = "share_preview"
	ShareArchivePreview     = "share_archive_preview"
	ShareForceProxy         = "share_force_proxy"
	ShareSummaryContent     = "share_summary_content"
	HandleHookAfterWriting  = "handle_hook_after_writing"
	HandleHookRateLimit     = "handle_hook_rate_limit"
	IgnoreSystemFiles       = "ignore_system_files"

	// index
	SearchIndex     = "search_index"
	AutoUpdateIndex = "auto_update_index"
	IgnorePaths     = "ignore_paths"
	MaxIndexDepth   = "max_index_depth"

	// aria2
	Aria2Uri    = "aria2_uri"
	Aria2Secret = "aria2_secret"

	// transmission
	TransmissionUri      = "transmission_uri"
	TransmissionSeedtime = "transmission_seedtime"

	// 115
	Pan115TempDir = "115_temp_dir"

	// 123
	Pan123TempDir = "123_temp_dir"

	// 115_open
	Pan115OpenTempDir = "115_open_temp_dir"

	// pikpak
	PikPakTempDir = "pikpak_temp_dir"

	// thunder
	ThunderTempDir = "thunder_temp_dir"

	// thunderx
	ThunderXTempDir = "thunderx_temp_dir"

	// thunder_browser
	ThunderBrowserTempDir = "thunder_browser_temp_dir"

	// single
	Token         = "token"
	IndexProgress = "index_progress"

	// SSO
	SSOClientId          = "sso_client_id"
	SSOClientSecret      = "sso_client_secret"
	SSOLoginEnabled      = "sso_login_enabled"
	SSOLoginPlatform     = "sso_login_platform"
	SSOOIDCUsernameKey   = "sso_oidc_username_key"
	SSOOrganizationName  = "sso_organization_name"
	SSOApplicationName   = "sso_application_name"
	SSOEndpointName      = "sso_endpoint_name"
	SSOJwtPublicKey      = "sso_jwt_public_key"
	SSOExtraScopes       = "sso_extra_scopes"
	SSOAutoRegister      = "sso_auto_register"
	SSODefaultDir        = "sso_default_dir"
	SSODefaultPermission = "sso_default_permission"
	SSOCompatibilityMode = "sso_compatibility_mode"

	// ldap
	LdapLoginEnabled      = "ldap_login_enabled"
	LdapServer            = "ldap_server"
	LdapSkipTlsVerify     = "ldap_skip_tls_verify"
	LdapManagerDN         = "ldap_manager_dn"
	LdapManagerPassword   = "ldap_manager_password"
	LdapUserSearchBase    = "ldap_user_search_base"
	LdapUserSearchFilter  = "ldap_user_search_filter"
	LdapDefaultPermission = "ldap_default_permission"
	LdapDefaultDir        = "ldap_default_dir"
	LdapLoginTips         = "ldap_login_tips"

	// s3
	S3Buckets         = "s3_buckets"
	S3AccessKeyId     = "s3_access_key_id"
	S3SecretAccessKey = "s3_secret_access_key"

	// qbittorrent
	QbittorrentUrl      = "qbittorrent_url"
	QbittorrentSeedtime = "qbittorrent_seedtime"

	// 123 open offline download
	Pan123OpenOfflineDownloadCallbackUrl = "123_open_callback_url"
	Pan123OpenTempDir                    = "123_open_temp_dir"

	// ftp
	FTPPublicHost            = "ftp_public_host"
	FTPPasvPortMap           = "ftp_pasv_port_map"
	FTPMandatoryTLS          = "ftp_mandatory_tls"
	FTPImplicitTLS           = "ftp_implicit_tls"
	FTPTLSPrivateKeyPath     = "ftp_tls_private_key_path"
	FTPTLSPublicCertPath     = "ftp_tls_public_cert_path"
	SFTPDisablePasswordLogin = "sftp_disable_password_login"

	// traffic
	TaskOfflineDownloadThreadsNum         = "offline_download_task_threads_num"
	TaskOfflineDownloadTransferThreadsNum = "offline_download_transfer_task_threads_num"
	TaskUploadThreadsNum                  = "upload_task_threads_num"
	TaskCopyThreadsNum                    = "copy_task_threads_num"
	TaskMoveThreadsNum                    = "move_task_threads_num"
	TaskDecompressDownloadThreadsNum      = "decompress_download_task_threads_num"
	TaskDecompressUploadThreadsNum        = "decompress_upload_task_threads_num"
	StreamMaxClientDownloadSpeed          = "max_client_download_speed"
	StreamMaxClientUploadSpeed            = "max_client_upload_speed"
	StreamMaxServerDownloadSpeed          = "max_server_download_speed"
	StreamMaxServerUploadSpeed            = "max_server_upload_speed"

	// media
	MediaTMDBKey          = "media_tmdb_key"
	MediaTMDBAPIURL       = "media_tmdb_api_url"
	MediaDiscogsToken     = "media_discogs_token"
	MediaDiscogsAPIURL    = "media_discogs_api_url"
	MediaThumbnailMode    = "media_thumbnail_mode"
	MediaThumbnailPath    = "media_thumbnail_path"
	MediaStoreThumbnail   = "media_store_thumbnail"
	MediaScrapeConcurrency = "media_scrape_concurrency"

	// transcode (云端/本地 FFmpeg 转码)
	TranscodeEnabled            = "transcode_enabled"              // 总开关，默认关
	TranscodeMinSizeGB          = "transcode_min_size_gb"          // 文件大于多少 GB 才转码
	TranscodeMinBitrateMbps     = "transcode_min_bitrate_mbps"     // 视频码率高于多少 Mbps 才转码（0=不限）
	TranscodeSourceCodecs       = "transcode_source_codecs"        // 仅对这些源编码进行转码
	TranscodeSourceExtensions   = "transcode_source_extensions"    // 仅对这些后缀进行转码
	TranscodeOutputFormat       = "transcode_output_format"        // 输出封装格式 hls/dash/mp4
	TranscodeOutputCodec        = "transcode_output_codec"         // 重新编码的视频编码
	TranscodeOutputBitrate      = "transcode_output_bitrate"       // 输出视频码率，例如 4000k
	TranscodeOutputAudioCodec   = "transcode_output_audio_codec"   // 输出音频编码
	TranscodeOutputAudioBitrate = "transcode_output_audio_bitrate" // 输出音频码率，例如 160k
	TranscodeOutputResolution   = "transcode_output_resolution"    // 输出分辨率上限
	TranscodeSegmentDuration    = "transcode_segment_duration"     // HLS 切片时长(秒)
	TranscodeHWAccel            = "transcode_hwaccel"              // 硬件加速类型 none/auto/nvenc/qsv/vaapi/amf/videotoolbox
	TranscodeFFmpegPath         = "transcode_ffmpeg_path"          // 自定义 ffmpeg 可执行路径
	TranscodeFFprobePath        = "transcode_ffprobe_path"         // 自定义 ffprobe 可执行路径
	TranscodeRunMode            = "transcode_run_mode"             // local / remote / hybrid
	TranscodeWorkerSecret       = "transcode_worker_secret"        // 远程 Worker 共享密钥
	TranscodeCachePath          = "transcode_cache_path"           // 切片缓存目录
	TranscodeCacheMaxGB         = "transcode_cache_max_gb"         // 缓存最大占用，超出 LRU 淘汰
	TranscodeJobTimeoutMin      = "transcode_job_timeout_min"      // 单个任务超时分钟
	TranscodeLocalConcurrency   = "transcode_local_concurrency"    // 本地内置 worker 并发数(run_mode=local/hybrid 时生效)
	TranscodeIdleTimeoutSec     = "transcode_idle_timeout_sec"     // 播放端无请求多少秒后自动停止转码（默认90秒）
)

const (
	UNKNOWN = iota
	FOLDER
	// OFFICE
	VIDEO
	AUDIO
	TEXT
	IMAGE
)

// ContextKey is the type of context keys.
type ContextKey int8

const (
	_ ContextKey = iota

	NoTaskKey
	ApiUrlKey
	UserKey
	MetaKey
	MetaPassKey
	ClientIPKey
	ProxyHeaderKey
	RequestHeaderKey
	UserAgentKey
	PathKey
	SharingIDKey
	SkipHookKey
)
