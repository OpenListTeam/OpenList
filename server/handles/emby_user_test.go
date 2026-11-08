package handles

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// /Users/Public 与登录接口必须返回同一种 UserDto 结构：
// 严格类型的 Emby 客户端会把 Policy / Configuration 直接强转成 Map，
// 缺字段就会抛 null 异常并中断渲染（issue #3301）。
func TestEmbyUserDtoHasPolicyAndConfiguration(t *testing.T) {
	u := &model.User{Username: "alice", PwdHash: "x"}
	dto := embyUserDtoOf(u)

	if dto.Policy == nil {
		t.Fatal("Policy 不能为空：客户端会读 EnableMediaPlayback")
	}
	if dto.Policy["EnableMediaPlayback"] != true {
		t.Fatalf("EnableMediaPlayback = %v, want true", dto.Policy["EnableMediaPlayback"])
	}
	if dto.Configuration == nil {
		t.Fatal("Configuration 不能为空：客户端会直接读字段")
	}
	if dto.Configuration["PlayDefaultAudioTrack"] != true {
		t.Fatalf("Configuration 缺少默认值: %+v", dto.Configuration)
	}
	if dto.Id == "" || dto.ServerId == "" {
		t.Fatal("Id / ServerId 不能为空")
	}
	if !dto.HasPassword {
		t.Fatal("HasPassword 应反映 PwdHash 是否为空")
	}
}
