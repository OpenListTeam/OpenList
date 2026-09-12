package crypt

import "github.com/OpenListTeam/OpenList/v4/internal/model"

type thumbObject struct {
	model.Object
	thumbPath string
	sourceObj model.Obj
}
