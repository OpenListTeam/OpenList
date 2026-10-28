package handles

import (
	"reflect"
	"testing"
)

func TestSeedConvertTargets(t *testing.T) {
	cases := []struct {
		name string
		req  SeedConvertReq
		want []string
	}{
		{
			name: "单个目标格式",
			req:  SeedConvertReq{TargetFormat: "torrent"},
			want: []string{"torrent"},
		},
		{
			name: "多选 + 去重 + 别名",
			req:  SeedConvertReq{Formats: []string{"bt", "oss", "BT", "cas"}},
			want: []string{"torrent", "oss", "cas"},
		},
		{
			name: "最多三个目标",
			req:  SeedConvertReq{Formats: []string{"oss", "torrent", "cas", "oss"}},
			want: []string{"oss", "torrent", "cas"},
		},
		{
			name: "非法值被忽略",
			req:  SeedConvertReq{Formats: []string{"zip", "OSS"}},
			want: []string{"oss"},
		},
		{
			name: "空请求",
			req:  SeedConvertReq{},
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := seedConvertTargets(c.req)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("seedConvertTargets = %v, want %v", got, c.want)
			}
		})
	}
}
