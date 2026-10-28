package handles

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/gin-gonic/gin"
)

// crossingSeed 构造一个「pieces 跨文件」的多文件种子：每个文件大小都不是
// piece size 的整数倍，因此按文件存的 pieces 无法拼回整包 torrent。
func crossingSeed() *torrent.Seed {
	const piece = int64(1 << 20)
	seed := torrent.NewSeed("crossing", "OpenList", piece)
	seed.Files = []torrent.SeedFile{
		{
			Path: "a.pdf",
			Size: piece + 123,
			Hashes: torrent.SeedHashes{
				MD5:    "md5-a",
				Pieces: &torrent.SeedPieceHashes{SHA1: []string{"aa", "bb"}},
			},
		},
		{
			Path: "b.pdf",
			Size: piece + 456,
			Hashes: torrent.SeedHashes{
				MD5:    "md5-b",
				Pieces: &torrent.SeedPieceHashes{SHA1: []string{"cc", "dd"}},
			},
		},
	}
	return seed
}

func conversionState(t *testing.T, states gin.H, format string) gin.H {
	t.Helper()
	state, ok := states[format].(gin.H)
	if !ok {
		t.Fatalf("missing conversion state for %q", format)
	}
	return state
}

// TestSeedConversionStatesIdentityIsFeasible 回归：目标格式等于当前格式时必须可行。
// 之前对 torrent 种子算「转 torrent」也会跑整包诊断，合法的跨文件 pieces 被误报
// piece boundary crosses the next file，预览页就给自己的格式打了叉。
func TestSeedConversionStatesIdentityIsFeasible(t *testing.T) {
	states := seedConversionStates(crossingSeed(), "torrent")
	state := conversionState(t, states, "torrent")
	if feasible, _ := state["feasible"].(bool); !feasible {
		t.Fatalf("torrent -> torrent should be feasible, got missing = %v", state["missing"])
	}
	if missing, ok := state["missing"].([]string); !ok || len(missing) != 0 {
		t.Fatalf("identity conversion should report no diagnostics, got %#v", state["missing"])
	}
}

// TestSeedConversionStatesCrossFormatStillDiagnosed 确认身份特判没有掩盖真正的转换问题：
// 同一个种子转其他格式时仍应报告 pieces 跨文件。
func TestSeedConversionStatesCrossFormatStillDiagnosed(t *testing.T) {
	states := seedConversionStates(crossingSeed(), "oss")
	state := conversionState(t, states, "torrent")
	if feasible, _ := state["feasible"].(bool); feasible {
		t.Fatal("oss -> torrent with crossing pieces should stay infeasible")
	}
	if missing, _ := state["missing"].([]string); len(missing) == 0 {
		t.Fatal("expected diagnostics for crossing pieces")
	}
}
