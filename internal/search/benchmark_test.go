package search

import (
	"fmt"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/mq"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// oldLogic_Batchconvert 模拟优化前的逻辑：使用 append 和 MustSliceConvert
func oldLogic_Batchconvert(messages []mq.Message[ObjWithParent]) []model.SearchNode {
	// 使用 MustSliceConvert 创建中间切片
	objs := utils.MustSliceConvert(messages, func(src mq.Message[ObjWithParent]) ObjWithParent {
		return src.Content
	})
	// 使用 append 动态扩容
	var searchNodes []model.SearchNode
	for i := range objs {
		searchNodes = append(searchNodes, model.SearchNode{
			Parent: objs[i].Parent,
			Name:   objs[i].GetName(),
			IsDir:  objs[i].IsDir(),
			Size:   objs[i].GetSize(),
		})
	}
	return searchNodes
}

// newLogic_Batchconvert 模拟优化后的逻辑：预分配切片 + 直接索引赋值
func newLogic_Batchconvert(messages []mq.Message[ObjWithParent]) []model.SearchNode {
	if len(messages) == 0 {
		return nil
	}
	searchNodes := make([]model.SearchNode, len(messages))
	for i := range messages {
		obj := messages[i].Content
		searchNodes[i] = model.SearchNode{
			Parent: obj.Parent,
			Name:   obj.GetName(),
			IsDir:  obj.IsDir(),
			Size:   obj.GetSize(),
		}
	}
	return searchNodes
}

// generateTestMessages 生成多样化的测试数据
func generateTestMessages(count int) []mq.Message[ObjWithParent] {
	messages := make([]mq.Message[ObjWithParent], count)
	paths := []string{"/documents", "/images/photos", "/videos/2024", "/music", "/downloads/temp"}
	for i := 0; i < count; i++ {
		messages[i] = mq.Message[ObjWithParent]{
			Content: ObjWithParent{
				Parent: paths[i%len(paths)],
				Obj: &model.Object{
					Name:     fmt.Sprintf("file_%d.dat", i),
					Size:     int64(1024 * (i%100 + 1)),
					IsFolder: i%10 == 0,
				},
			},
		}
	}
	return messages
}

// BenchmarkSearchAllocation_Old 测试优化前的切片分配性能
func BenchmarkSearchAllocation_Old(b *testing.B) {
	sizes := []int{100, 1000, 10000}
	for _, size := range sizes {
		messages := generateTestMessages(size)
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = oldLogic_Batchconvert(messages)
			}
		})
	}
}

// BenchmarkSearchAllocation_New 测试优化后的切片分配性能
func BenchmarkSearchAllocation_New(b *testing.B) {
	sizes := []int{100, 1000, 10000}
	for _, size := range sizes {
		messages := generateTestMessages(size)
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = newLogic_Batchconvert(messages)
			}
		})
	}
}
