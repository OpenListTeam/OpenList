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

type RenameRequest struct {
	Name               string `json:"Name"`               // PGP encrypted name
	NameSignatureEmail string `json:"NameSignatureEmail"` // User's signature email
	Hash               string `json:"Hash"`               // New name hash
	OriginalHash       string `json:"OriginalHash"`       // Current name hash
}

type RenameResponse struct {
	Code int `json:"Code"`
}
