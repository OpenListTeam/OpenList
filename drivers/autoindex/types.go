package autoindex

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

var (
	errEmptyEvaluateResult = fmt.Errorf("empty result")
)

type exactSizeObj struct{ model.Obj }
