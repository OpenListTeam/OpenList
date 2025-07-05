package server

import (
	"time"

	"github.com/OpenListTeam/OpenList/internal/conf"
	"github.com/OpenListTeam/OpenList/server/dlna"
	"github.com/OpenListTeam/OpenList/server/dlna/dlnaflags"
)

func StartDlnaServer() error {
	err := dlna.Run(&dlnaflags.Options{
		ListenAddr:       conf.Conf.DLNA.Listen,
		FriendlyName:     conf.Conf.DLNA.FriendlyName,
		LogTrace:         false,
		InterfaceNames:   conf.Conf.DLNA.InterfaceNames,
		AnnounceInterval: time.Duration(conf.Conf.DLNA.AnnounceInterval) * time.Minute,
		RootDir:          conf.Conf.DLNA.RootDir,
	})
	if err != nil {
		return err
	}
	return nil
}
