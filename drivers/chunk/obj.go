package chunk

import "github.com/OpenListTeam/OpenList/v4/internal/model"

type chunkObject struct {
	model.Object
	chunkSizes []int64
}
