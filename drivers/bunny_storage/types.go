package bunny_storage

import "time"

type bunnyObject struct {
	Guid            string `json:"Guid"`
	StorageZoneName string `json:"StorageZoneName"`
	Path            string `json:"Path"`
	ObjectName      string `json:"ObjectName"`
	Length          int64  `json:"Length"`
	LastChanged     string `json:"LastChanged"`
	IsDirectory     bool   `json:"IsDirectory"`
	ServerID        int    `json:"ServerId"`
	UserID          string `json:"UserId"`
	DateCreated     string `json:"DateCreated"`
	StorageZoneID   int64  `json:"StorageZoneId"`
}

type apiError struct {
	HttpCode int    `json:"HttpCode"`
	Message  string `json:"Message"`
}

type parsedTimes struct {
	modified time.Time
	created  time.Time
}
