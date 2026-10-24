package teldrive_v2

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address string `json:"url" required:"true"`
	ApiKey  string `json:"api_key" type:"string" required:"true" help:"API key from TelDrive (Settings -> API keys). Sent as the X-Api-Key header."`

	UseShareLink bool `json:"use_share_link" type:"bool" default:"false" help:"Serve downloads as a direct 302 to TelDrive instead of streaming them through OpenList. Requires web proxy to be off. Each link carries a TelDrive share token, so anyone holding the URL can download that file without signing in, and OpenList can no longer count or revoke those accesses. Shares are left on your TelDrive until they expire, including after this storage is removed."`
	// A select rather than a number: an emptied number field arrives as "" and
	// fails to unmarshal into int64, which leaves the storage dead with a
	// message about readUint64 that means nothing to the person reading it.
	ShareExpire string `json:"share_expire" type:"select" options:"1,2,3,6,12,24,48,72,168" default:"1" help:"Only used when Use share link is on. How long each generated TelDrive link stays usable, in hours. This is not the expiry you set when sharing in OpenList; it only bounds how long a link that leaks keeps working. Raise it if long downloads are cut off."`

	// 512 MiB is what the server itself picks when preferredPartSize is omitted,
	// and what the reference rclone backend defaults to. Note that OpenList
	// buffers each in-flight part, so the working set is roughly
	// chunk_size x upload_concurrency - lower this on memory-tight hosts.
	ChunkSize         int64 `json:"chunk_size" type:"number" default:"512" help:"Preferred upload part size in MiB, rounded up to a multiple of 16 and capped at 2000. Smaller parts use less memory and move the progress bar more often."`
	UploadConcurrency int64 `json:"upload_concurrency" type:"number" default:"4" help:"Number of parts uploaded in parallel (max 16). Each one is buffered, so this multiplies chunk size."`

	// The server hashes every part regardless; this only controls whether we
	// also hash locally and make the server compare, catching corruption in
	// transit. Named after the reference rclone backend's option.
	HashEnabled bool `json:"hash_enabled" type:"bool" default:"true" help:"Send a BLAKE3 checksum with each part so the server verifies it. Costs one extra read of each part."`

	HardDelete bool `json:"hard_delete" type:"bool" default:"false" help:"Purge files permanently instead of moving them to trash. Trashed files stay recoverable, and their Telegram storage is only reclaimed when TelDrive's cleanup job expires them."`
}

var config = driver.Config{
	Name:        "Teldrive V2",
	DefaultRoot: "/",
	// v2 always resolves conflicts server-side via conflictPolicy
	NoOverwriteUpload: false,
	// Without UseShareLink the download URL is authenticated by the X-Api-Key
	// header, which a browser following a 302 cannot send - it just gets a 401.
	// Default to proxying so a freshly added storage works; turning UseShareLink
	// on is what makes direct 302 links viable.
	PreferProxy: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &TeldriveV2{}
	})
}
