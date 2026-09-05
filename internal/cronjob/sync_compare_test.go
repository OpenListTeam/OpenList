package cronjob

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// TestObjectsDifferUsesSizeWhenNoExplicitCompareIsSet 覆盖默认比较逻辑。
// 用户即使没有勾选比较字段，同步也不能把“目标已有同名文件”误判为完全一致后跳过所有更新。
func TestObjectsDifferUsesSizeWhenNoExplicitCompareIsSet(t *testing.T) {
	// 两个对象修改时间不同，但比较方式为空；默认仍按 size 比较。
	modified := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	src := &model.Object{Name: "file.txt", Size: 10, Modified: modified}
	dst := &model.Object{Name: "file.txt", Size: 10, Modified: modified.Add(time.Hour)}

	needSync, err := objectsDiffer(src, dst, SyncArgs{})
	if err != nil {
		t.Fatalf("unexpected compare error: %+v", err)
	}
	if needSync {
		t.Fatal("objects with the same size should not sync when no explicit compare is set")
	}

	// 改变源文件大小后，默认 size 比较必须触发更新。
	src.Size = 11
	needSync, err = objectsDiffer(src, dst, SyncArgs{})
	if err != nil {
		t.Fatalf("unexpected compare error: %+v", err)
	}
	if !needSync {
		t.Fatal("objects with different sizes should sync by default")
	}
}

// TestObjectsDifferWithExplicitSize 覆盖管理员显式开启 size 比较的场景。
func TestObjectsDifferWithExplicitSize(t *testing.T) {
	// 同大小、不同修改时间：只开启 size 时不应同步。
	src := &model.Object{Name: "file.txt", Size: 10, Modified: time.Now()}
	dst := &model.Object{Name: "file.txt", Size: 10, Modified: time.Now().Add(time.Hour)}

	needSync, err := objectsDiffer(src, dst, SyncArgs{Size: true})
	if err != nil {
		t.Fatalf("unexpected compare error: %+v", err)
	}
	if needSync {
		t.Fatal("explicit size compare should not sync same-size objects")
	}

	// 大小不同：必须同步。
	dst.Size = 20
	needSync, err = objectsDiffer(src, dst, SyncArgs{Size: true})
	if err != nil {
		t.Fatalf("unexpected compare error: %+v", err)
	}
	if !needSync {
		t.Fatal("explicit size compare should sync different-size objects")
	}
}

// TestObjectsDifferWithMTime 覆盖显式使用修改时间比较。
// 部分网盘的 mtime 不可靠，因此该字段必须是管理员显式开启，不能作为默认行为。
func TestObjectsDifferWithMTime(t *testing.T) {
	// 同大小但修改时间不同：mtime 比较必须触发同步。
	baseTime := time.Date(2026, 9, 5, 9, 30, 0, 0, time.UTC)
	src := &model.Object{Name: "file.txt", Size: 10, Modified: baseTime}
	dst := &model.Object{Name: "file.txt", Size: 10, Modified: baseTime.Add(time.Minute)}

	needSync, err := objectsDiffer(src, dst, SyncArgs{MTime: true})
	if err != nil {
		t.Fatalf("unexpected compare error: %+v", err)
	}
	if !needSync {
		t.Fatal("mtime compare should sync objects with different modification times")
	}

	// 修改时间完全相同：不应同步。
	dst.Modified = baseTime
	needSync, err = objectsDiffer(src, dst, SyncArgs{MTime: true})
	if err != nil {
		t.Fatalf("unexpected compare error: %+v", err)
	}
	if needSync {
		t.Fatal("mtime compare should not sync objects with the same modification time")
	}
}

// TestObjectsDifferWithChecksum 覆盖共同哈希类型的比较。
// 云存储通常会返回 md5/sha1/sha256 中的若干类型；只有两侧都存在同一类型时才能比较。
func TestObjectsDifferWithChecksum(t *testing.T) {
	// 使用项目注册的 md5 hash 类型，避免自己造一个无法被 HashInfo 识别的类型。
	md5Hash, ok := utils.GetHashByName("md5")
	if !ok || md5Hash == nil {
		t.Skip("md5 hash type is not registered")
	}

	// 相同哈希且相同大小：checksum 判定无需同步。
	src := &model.Object{
		Name:     "file.txt",
		Size:     10,
		Modified: time.Now(),
		HashInfo: utils.NewHashInfo(md5Hash, "same-hash"),
	}
	dst := &model.Object{
		Name:     "file.txt",
		Size:     10,
		Modified: time.Now().Add(time.Hour),
		HashInfo: utils.NewHashInfo(md5Hash, "same-hash"),
	}

	needSync, err := objectsDiffer(src, dst, SyncArgs{Checksum: true})
	if err != nil {
		t.Fatalf("unexpected checksum compare error: %+v", err)
	}
	if needSync {
		t.Fatal("checksum compare should not sync objects with the same hash")
	}

	// 相同大小但哈希不同：checksum 判定必须同步。
	dst.HashInfo = utils.NewHashInfo(md5Hash, "different-hash")
	needSync, err = objectsDiffer(src, dst, SyncArgs{Checksum: true})
	if err != nil {
		t.Fatalf("unexpected checksum compare error: %+v", err)
	}
	if !needSync {
		t.Fatal("checksum compare should sync objects with different hashes")
	}
}

// TestHashesEqualRequireSameHashType 覆盖哈希比较的边界。
// 只有一侧提供哈希时不能判定为“相同”，必须返回 comparable=false。
func TestHashesEqualRequireSameHashType(t *testing.T) {
	md5Hash, ok := utils.GetHashByName("md5")
	if !ok || md5Hash == nil {
		t.Skip("md5 hash type is not registered")
	}

	src := &model.Object{HashInfo: utils.NewHashInfo(md5Hash, "hash")}
	dstWithoutHash := &model.Object{}
	same, comparable, err := hashesEqual(src, dstWithoutHash)
	if err != nil {
		t.Fatalf("unexpected hashesEqual error: %+v", err)
	}
	if comparable || same {
		t.Fatal("missing destination hash should not be treated as comparable")
	}

	// 两侧都有共同哈希时，字符串不同必须返回 false。
	dstDifferent := &model.Object{HashInfo: utils.NewHashInfo(md5Hash, "other-hash")}
	same, comparable, err = hashesEqual(src, dstDifferent)
	if err != nil {
		t.Fatalf("unexpected hashesEqual error: %+v", err)
	}
	if !comparable {
		t.Fatal("same hash type should be comparable")
	}
	if same {
		t.Fatal("different hash values should not be equal")
	}
}

// TestRelativePath 覆盖同步相对路径计算。
// 相对路径会传给 exclude/regexp 过滤器，路径错误会导致过滤规则命中错误对象。
func TestRelativePath(t *testing.T) {
	cases := []struct {
		name string
		root string
		path string
		want string
	}{
		{name: "root itself", root: "/sync/src", path: "/sync/src", want: ""},
		{name: "first level file", root: "/sync/src", path: "/sync/src/file.txt", want: "file.txt"},
		{name: "nested file", root: "/sync/src", path: "/sync/src/dir/file.txt", want: "dir/file.txt"},
		{name: "file root", root: "/sync/src/file.txt", path: "/sync/src/file.txt", want: ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := relativePath(tt.root, tt.path); got != tt.want {
				t.Fatalf("relativePath(%q, %q) = %q, want %q", tt.root, tt.path, got, tt.want)
			}
		})
	}
}

// TestIsPathInside 覆盖路径互套检测的底层函数。
// 该函数是防止源目录和目标目录互相覆盖的关键保护。
func TestIsPathInside(t *testing.T) {
	cases := []struct {
		name   string
		child  string
		parent string
		want   bool
	}{
		{name: "same path", child: "/sync/src", parent: "/sync/src", want: true},
		{name: "child under parent", child: "/sync/src/child", parent: "/sync/src", want: true},
		{name: "same prefix but another directory", child: "/sync/src-other", parent: "/sync/src", want: false},
		{name: "outside parent", child: "/sync/other", parent: "/sync/src", want: false},
		{name: "clean parent traversal", child: "/sync/src", parent: "/sync/src/../src", want: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPathInside(tt.child, tt.parent); got != tt.want {
				t.Fatalf("isPathInside(%q, %q) = %v, want %v", tt.child, tt.parent, got, tt.want)
			}
		})
	}
}

// TestSyncFiltersExcludedAndBoundaries 覆盖过滤器的实际匹配边界。
// 这些函数在每次同步递归时都会使用，能提前发现配置语法和边界判断问题。
func TestSyncFiltersExcludedAndBoundaries(t *testing.T) {
	filters, err := newSyncFilters(SyncArgs{
		Exclude:        []string{"*.tmp", "cache/**"},
		ExcludeRegexp:  []string{`(^|/)secret-.*\.bin$`},
		ExcludeRegexp2: []string{`ignore-(?i)case`},
		MaxSize:        100,
		MinSize:        10,
		MaxAge:         "2h",
		MinAge:         "1m",
	})
	if err != nil {
		t.Fatalf("failed create filters: %+v", err)
	}

	// Glob：第一层、子目录、以及仅匹配 basename 的路径都应命中。
	for _, path := range []string{"bad.tmp", "nested/bad.tmp", "cache/file.txt"} {
		if !filters.excluded(path) {
			t.Fatalf("path %q should be excluded", path)
		}
	}
	// Go regexp 和 regexp2（大小写关闭）也应命中。
	for _, path := range []string{"dir/secret-key.bin", "ignore-CASE.txt"} {
		if !filters.excluded(path) {
			t.Fatalf("path %q should be excluded", path)
		}
	}
	// 正常文件不应被误伤。
	if filters.excluded("normal/file.txt") {
		t.Fatal("normal file should not be excluded")
	}

	// 大小边界：>= max、<= min 都会被排除；开区间内的文件保留。
	if !filters.sizeExcluded(&model.Object{Size: 100}) {
		t.Fatal("file equal to max_size should be excluded")
	}
	if !filters.sizeExcluded(&model.Object{Size: 10}) {
		t.Fatal("file equal to min_size should be excluded")
	}
	if filters.sizeExcluded(&model.Object{Size: 50}) {
		t.Fatal("file between min_size and max_size should not be excluded")
	}

	// 年龄边界：old >= max_age 排除；new <= min_age 排除。
	old := time.Now().Add(-3 * time.Hour)
	newObj := time.Now().Add(-30 * time.Second)
	if !filters.ageExcluded(&model.Object{Modified: old}) {
		t.Fatal("old file should be excluded by max_age")
	}
	if !filters.ageExcluded(&model.Object{Modified: newObj}) {
		t.Fatal("new file should be excluded by min_age")
	}
	if filters.ageExcluded(&model.Object{Modified: time.Now().Add(-30 * time.Minute)}) {
		t.Fatal("file inside age range should not be excluded")
	}
}
