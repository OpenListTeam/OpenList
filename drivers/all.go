package drivers

import (
	d115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	d115_open "github.com/OpenListTeam/OpenList/v4/drivers/115_open"
	d115_share "github.com/OpenListTeam/OpenList/v4/drivers/115_share"
	d123 "github.com/OpenListTeam/OpenList/v4/drivers/123"
	d123_link "github.com/OpenListTeam/OpenList/v4/drivers/123_link"
	d123_open "github.com/OpenListTeam/OpenList/v4/drivers/123_open"
	d123_share "github.com/OpenListTeam/OpenList/v4/drivers/123_share"
	d139 "github.com/OpenListTeam/OpenList/v4/drivers/139"
	d189 "github.com/OpenListTeam/OpenList/v4/drivers/189"
	d189_tv "github.com/OpenListTeam/OpenList/v4/drivers/189_tv"
	d189pc "github.com/OpenListTeam/OpenList/v4/drivers/189pc"
	alias "github.com/OpenListTeam/OpenList/v4/drivers/alias"
	alidoc "github.com/OpenListTeam/OpenList/v4/drivers/alidoc"
	alist_v3 "github.com/OpenListTeam/OpenList/v4/drivers/alist_v3"
	aliyundrive "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive"
	aliyundrive_open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	aliyundrive_share "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share"
	autoindex "github.com/OpenListTeam/OpenList/v4/drivers/autoindex"
	azure_blob "github.com/OpenListTeam/OpenList/v4/drivers/azure_blob"
	baidu_netdisk "github.com/OpenListTeam/OpenList/v4/drivers/baidu_netdisk"
	baidu_photo "github.com/OpenListTeam/OpenList/v4/drivers/baidu_photo"
	bunny_storage "github.com/OpenListTeam/OpenList/v4/drivers/bunny_storage"
	chaoxing "github.com/OpenListTeam/OpenList/v4/drivers/chaoxing"
	chunk "github.com/OpenListTeam/OpenList/v4/drivers/chunk"
	cloudflare_imgbed "github.com/OpenListTeam/OpenList/v4/drivers/cloudflare_imgbed"
	cloudreve "github.com/OpenListTeam/OpenList/v4/drivers/cloudreve"
	cloudreve_v4 "github.com/OpenListTeam/OpenList/v4/drivers/cloudreve_v4"
	cnb_releases "github.com/OpenListTeam/OpenList/v4/drivers/cnb_releases"
	crypt "github.com/OpenListTeam/OpenList/v4/drivers/crypt"
	degoo "github.com/OpenListTeam/OpenList/v4/drivers/degoo"
	doubao "github.com/OpenListTeam/OpenList/v4/drivers/doubao"
	doubao_new "github.com/OpenListTeam/OpenList/v4/drivers/doubao_new"
	doubao_share "github.com/OpenListTeam/OpenList/v4/drivers/doubao_share"
	dropbox "github.com/OpenListTeam/OpenList/v4/drivers/dropbox"
	emby "github.com/OpenListTeam/OpenList/v4/drivers/emby"
	febbox "github.com/OpenListTeam/OpenList/v4/drivers/febbox"
	ftp "github.com/OpenListTeam/OpenList/v4/drivers/ftp"
	github "github.com/OpenListTeam/OpenList/v4/drivers/github"
	github_releases "github.com/OpenListTeam/OpenList/v4/drivers/github_releases"
	google_drive "github.com/OpenListTeam/OpenList/v4/drivers/google_drive"
	google_photo "github.com/OpenListTeam/OpenList/v4/drivers/google_photo"
	guangyapan "github.com/OpenListTeam/OpenList/v4/drivers/guangyapan"
	halalcloud "github.com/OpenListTeam/OpenList/v4/drivers/halalcloud"
	halalcloud_open "github.com/OpenListTeam/OpenList/v4/drivers/halalcloud_open"
	ilanzou "github.com/OpenListTeam/OpenList/v4/drivers/ilanzou"
	ipfs_api "github.com/OpenListTeam/OpenList/v4/drivers/ipfs_api"
	kodbox "github.com/OpenListTeam/OpenList/v4/drivers/kodbox"
	lanzou "github.com/OpenListTeam/OpenList/v4/drivers/lanzou"
	lenovonas_share "github.com/OpenListTeam/OpenList/v4/drivers/lenovonas_share"
	local "github.com/OpenListTeam/OpenList/v4/drivers/local"
	mediafire "github.com/OpenListTeam/OpenList/v4/drivers/mediafire"
	mediatrack "github.com/OpenListTeam/OpenList/v4/drivers/mediatrack"
	mega "github.com/OpenListTeam/OpenList/v4/drivers/mega"
	misskey "github.com/OpenListTeam/OpenList/v4/drivers/misskey"
	mopan "github.com/OpenListTeam/OpenList/v4/drivers/mopan"
	netease_music "github.com/OpenListTeam/OpenList/v4/drivers/netease_music"
	onedrive "github.com/OpenListTeam/OpenList/v4/drivers/onedrive"
	onedrive_app "github.com/OpenListTeam/OpenList/v4/drivers/onedrive_app"
	onedrive_sharelink "github.com/OpenListTeam/OpenList/v4/drivers/onedrive_sharelink"
	openlist "github.com/OpenListTeam/OpenList/v4/drivers/openlist"
	openlist_share "github.com/OpenListTeam/OpenList/v4/drivers/openlist_share"
	pikpak "github.com/OpenListTeam/OpenList/v4/drivers/pikpak"
	pikpak_share "github.com/OpenListTeam/OpenList/v4/drivers/pikpak_share"
	proton_drive "github.com/OpenListTeam/OpenList/v4/drivers/proton_drive"
	quark_open "github.com/OpenListTeam/OpenList/v4/drivers/quark_open"
	quark_uc "github.com/OpenListTeam/OpenList/v4/drivers/quark_uc"
	quark_uc_tv "github.com/OpenListTeam/OpenList/v4/drivers/quark_uc_tv"
	s3 "github.com/OpenListTeam/OpenList/v4/drivers/s3"
	seafile "github.com/OpenListTeam/OpenList/v4/drivers/seafile"
	sftp "github.com/OpenListTeam/OpenList/v4/drivers/sftp"
	smb "github.com/OpenListTeam/OpenList/v4/drivers/smb"
	strm "github.com/OpenListTeam/OpenList/v4/drivers/strm"
	teambition "github.com/OpenListTeam/OpenList/v4/drivers/teambition"
	teldrive "github.com/OpenListTeam/OpenList/v4/drivers/teldrive"
	terabox "github.com/OpenListTeam/OpenList/v4/drivers/terabox"
	thunder "github.com/OpenListTeam/OpenList/v4/drivers/thunder"
	thunder_browser "github.com/OpenListTeam/OpenList/v4/drivers/thunder_browser"
	thunderx "github.com/OpenListTeam/OpenList/v4/drivers/thunderx"
	url_tree "github.com/OpenListTeam/OpenList/v4/drivers/url_tree"
	uss "github.com/OpenListTeam/OpenList/v4/drivers/uss"
	virtual "github.com/OpenListTeam/OpenList/v4/drivers/virtual"
	webdav "github.com/OpenListTeam/OpenList/v4/drivers/webdav"
	weiyun "github.com/OpenListTeam/OpenList/v4/drivers/weiyun"
	wopan "github.com/OpenListTeam/OpenList/v4/drivers/wopan"
	wps "github.com/OpenListTeam/OpenList/v4/drivers/wps"
	yandex_disk "github.com/OpenListTeam/OpenList/v4/drivers/yandex_disk"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

func All() []driver.Constructor {
	return []driver.Constructor{
		d115.New,
		d115_open.New,
		d115_share.New,
		d123.New,
		d123_link.New,
		d123_open.New,
		d123_share.New,
		d139.New,
		d189.New,
		d189_tv.New,
		d189pc.New,
		alias.New,
		alidoc.New,
		alist_v3.New,
		aliyundrive.New,
		aliyundrive_open.New,
		aliyundrive_share.New,
		autoindex.New,
		azure_blob.New,
		baidu_netdisk.New,
		baidu_photo.New,
		bunny_storage.New,
		chaoxing.New,
		chunk.New,
		cloudflare_imgbed.New,
		cloudreve.New,
		cloudreve_v4.New,
		cnb_releases.New,
		crypt.New,
		degoo.New,
		doubao.New,
		doubao_new.New,
		doubao_share.New,
		dropbox.New,
		emby.New,
		febbox.New,
		ftp.New,
		github.New,
		github_releases.New,
		google_drive.New,
		google_photo.New,
		guangyapan.New,
		halalcloud.New,
		halalcloud_open.New,
		ilanzou.New,
		ilanzou.New2,
		ipfs_api.New,
		kodbox.New,
		lanzou.New,
		lenovonas_share.New,
		local.New,
		mediafire.New,
		mediatrack.New,
		mega.New,
		misskey.New,
		mopan.New,
		netease_music.New,
		onedrive.New,
		onedrive_app.New,
		onedrive_sharelink.New,
		openlist.New,
		openlist_share.New,
		pikpak.New,
		pikpak_share.New,
		proton_drive.New,
		quark_open.New,
		quark_uc.New,
		quark_uc.New2,
		quark_uc_tv.New,
		quark_uc_tv.New2,
		s3.New,
		s3.New2,
		seafile.New,
		sftp.New,
		smb.New,
		strm.New,
		teambition.New,
		teldrive.New,
		terabox.New,
		thunder.New,
		thunder.New2,
		thunder_browser.New,
		thunder_browser.New2,
		thunderx.New,
		thunderx.New2,
		url_tree.New,
		uss.New,
		virtual.New,
		webdav.New,
		weiyun.New,
		wopan.New,
		wps.New,
		yandex_disk.New,
	}
}
