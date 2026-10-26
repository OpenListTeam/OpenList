package task_test

import (
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
)

func TestTaskExtensionRestoresAPIURL(t *testing.T) {
	const want = "https://openlist.example"
	extension := task.TaskExtension{ApiUrl: want}

	extension.SetCtx(context.Background())

	if got := conf.GetApiUrl(extension.Ctx()); got != want {
		t.Fatalf("restored origin = %q, want %q", got, want)
	}
}
