package protondrive

/*
Package protondrive
Author: Da3zKi7<da3zki7@duck.com>
Date: 2025-09-18

Thanks to @henrybear327 for modded go-proton-api & Proton-API-Bridge

The power of open-source, the force of teamwork and the magic of reverse engineering!


D@' 3z K!7 - The King Of Cracking

Да здравствует Родина))
*/

import (
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/henrybear327/go-proton-api"
)

type ProtonFile struct {
	*proton.Link
	Name     string
	IsFolder bool
}

func (p *ProtonFile) GetName() string {
	return p.Name
}

func (p *ProtonFile) GetSize() int64 {
	return p.Link.Size
}

func (p *ProtonFile) GetPath() string {
	return p.Name
}

func (p *ProtonFile) IsDir() bool {
	return p.IsFolder
}

func (p *ProtonFile) ModTime() time.Time {
	return time.Unix(p.Link.ModifyTime, 0)
}

func (p *ProtonFile) CreateTime() time.Time {
	return time.Unix(p.Link.CreateTime, 0)
}

type downloadInfo struct {
	LinkID   string
	FileName string
}

type httpRange struct {
	start, end int64
}

type MoveRequest struct {
	ParentLinkID            string  `json:"ParentLinkID"`
	NodePassphrase          string  `json:"NodePassphrase"`
	NodePassphraseSignature *string `json:"NodePassphraseSignature"`
	Name                    string  `json:"Name"`
	NameSignatureEmail      string  `json:"NameSignatureEmail"`
	Hash                    string  `json:"Hash"`
	OriginalHash            string  `json:"OriginalHash"`
	ContentHash             *string `json:"ContentHash"` // Maybe null
}

type progressReader struct {
	reader   io.Reader
	total    int64
	current  int64
	callback driver.UpdateProgress
}

type RenameRequest struct {
	Name               string `json:"Name"`               // PGP encrypted name
	NameSignatureEmail string `json:"NameSignatureEmail"` // User's signature email
	Hash               string `json:"Hash"`               // New name hash
	OriginalHash       string `json:"OriginalHash"`       // Current name hash
}

type RenameResponse struct {
	Code int `json:"Code"`
}
