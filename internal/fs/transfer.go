package fs

import (
	"context"

	log "github.com/sirupsen/logrus"
)

func Transfer(ctx context.Context, dstPath, shareURL, validCode string) error {
	if shareURL == "" {
		return nil
	}

	err := transfer_share(ctx, dstPath, shareURL, validCode)
	if err != nil {
		log.Errorf("failed transfer %s: %+v", dstPath, err)
		return err
	}

	return nil
}
