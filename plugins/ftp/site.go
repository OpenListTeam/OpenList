package ftp

import (
	"fmt"
	"strconv"

	ftpserver "github.com/fclairamb/ftpserverlib"
)

func HandleSIZE(param string, client ftpserver.ClientDriver) (int, string) {
	fs, ok := client.(*AferoAdapter)
	if !ok {
		return ftpserver.StatusNotLoggedIn, "Unexpected exception (driver is nil)"
	}
	size, err := strconv.ParseInt(param, 10, 64)
	if err != nil {
		return ftpserver.StatusSyntaxErrorParameters, fmt.Sprintf(
			"Couldn't parse file size, given: %s, err: %v", param, err)
	}
	fs.SetNextFileSize(size)
	return ftpserver.StatusOK, "Accepted next file size"
}
