package common

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestCoversPath(t *testing.T) {
	tests := []struct {
		name     string
		metaPath string
		reqPath  string
		applySub bool
		want     bool
	}{
		{
			name:     "exact path match with applySub=false",
			metaPath: "/folder",
			reqPath:  "/folder",
			applySub: false,
			want:     true,
		},
		{
			name:     "exact path match with applySub=true",
			metaPath: "/folder",
			reqPath:  "/folder",
			applySub: true,
			want:     true,
		},
		{
			name:     "sub path with applySub=true",
			metaPath: "/folder",
			reqPath:  "/folder/subfolder",
			applySub: true,
			want:     true,
		},
		{
			name:     "sub path with applySub=false",
			metaPath: "/folder",
			reqPath:  "/folder/subfolder",
			applySub: false,
			want:     false,
		},
		{
			name:     "non-sub path with applySub=true",
			metaPath: "/folder",
			reqPath:  "/other",
			applySub: true,
			want:     false,
		},
		{
			name:     "non-sub path with applySub=false",
			metaPath: "/folder",
			reqPath:  "/other",
			applySub: false,
			want:     false,
		},
		{
			name:     "root path covers all with applySub=true",
			metaPath: "/",
			reqPath:  "/any/deep/path",
			applySub: true,
			want:     true,
		},
		{
			name:     "root path exact match",
			metaPath: "/",
			reqPath:  "/",
			applySub: false,
			want:     true,
		},
		{
			name:     "deep sub path with applySub=true",
			metaPath: "/folder",
			reqPath:  "/folder/sub1/sub2/file.txt",
			applySub: true,
			want:     true,
		},
		{
			name:     "sibling paths with applySub=true",
			metaPath: "/folder1",
			reqPath:  "/folder2",
			applySub: true,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MetaCoversPath(tt.metaPath, tt.reqPath, tt.applySub)
			if got != tt.want {
				t.Errorf("MetaCoversPath(%q, %q, %v) = %v, want %v",
					tt.metaPath, tt.reqPath, tt.applySub, got, tt.want)
			}
		})
	}
}

func TestCanWriteContentIgnoringUserPerms(t *testing.T) {
	tests := []struct {
		name   string
		meta   *model.Meta
		path   string
		want   bool
		reason string
	}{
		{
			name:   "nil meta",
			meta:   nil,
			path:   "/any",
			want:   false,
			reason: "nil meta should deny write",
		},
		{
			name: "meta.Write=false",
			meta: &model.Meta{
				Path:  "/folder",
				Write: false,
			},
			path:   "/folder",
			want:   false,
			reason: "Write=false should deny write",
		},
		{
			name: "exact path match with WSub=false",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  false,
			},
			path:   "/folder",
			want:   true,
			reason: "exact path match should allow write",
		},
		{
			name: "sub path with WSub=true",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  true,
			},
			path:   "/folder/subfolder",
			want:   true,
			reason: "sub path with WSub=true should allow write",
		},
		{
			name: "sub path with WSub=false (BEHAVIOR CHANGE)",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  false,
			},
			path:   "/folder/subfolder",
			want:   false,
			reason: "sub path with WSub=false should deny write (fixed bug)",
		},
		{
			name: "non-sub path with WSub=true",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  true,
			},
			path:   "/other",
			want:   false,
			reason: "non-sub path should deny write even with WSub=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanWriteContentBypassUserPerms(tt.meta, tt.path)
			if got != tt.want {
				t.Errorf("CanWriteContentBypassUserPerms() = %v, want %v\nReason: %s",
					got, tt.want, tt.reason)
			}
		})
	}
}

func TestCanRead(t *testing.T) {
	tests := []struct {
		name   string
		user   *model.User
		meta   *model.Meta
		path   string
		want   bool
		reason string
	}{
		{
			name:   "nil user should deny access",
			user:   nil,
			meta:   nil,
			path:   "/any",
			want:   false,
			reason: "nil user should not have read access",
		},
		{
			name: "nil meta should allow access",
			user: &model.User{
				ID: 1,
			},
			meta:   nil,
			path:   "/any",
			want:   true,
			reason: "nil meta means no restrictions",
		},
		{
			name: "empty ReadUsers list should allow access",
			user: &model.User{
				ID: 1,
			},
			meta: &model.Meta{
				Path:      "/folder",
				ReadUsers: []uint{},
			},
			path:   "/folder",
			want:   true,
			reason: "empty ReadUsers means no user-level restrictions",
		},
		{
			name: "user in ReadUsers list with exact path match",
			user: &model.User{
				ID: 1,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: false,
			},
			path:   "/folder",
			want:   true,
			reason: "user ID 1 is in ReadUsers list",
		},
		{
			name: "user not in ReadUsers list with exact path match",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: false,
			},
			path:   "/folder",
			want:   false,
			reason: "user ID 5 is not in ReadUsers list and path matches",
		},
		{
			name: "user not in ReadUsers list with ReadUsersSub=true for sub path",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: true,
			},
			path:   "/folder/subfolder",
			want:   false,
			reason: "user ID 5 is not in ReadUsers list and ReadUsersSub applies to sub paths",
		},
		{
			name: "user not in ReadUsers list with ReadUsersSub=false for sub path",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: false,
			},
			path:   "/folder/subfolder",
			want:   true,
			reason: "ReadUsersSub=false means restriction doesn't apply to sub paths",
		},
		{
			name: "user in ReadUsers list with ReadUsersSub=true for sub path",
			user: &model.User{
				ID: 2,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: true,
			},
			path:   "/folder/subfolder/deep",
			want:   true,
			reason: "user ID 2 is in ReadUsers list so can access sub paths",
		},
		{
			name: "user not in ReadUsers list for different path",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: false,
			},
			path:   "/other",
			want:   true,
			reason: "meta path doesn't match request path, so restriction doesn't apply",
		},
		{
			name: "root level restriction with ReadUsersSub=true",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:         "/",
				ReadUsers:    []uint{1, 2, 3},
				ReadUsersSub: true,
			},
			path:   "/any/deep/path",
			want:   false,
			reason: "root level restriction with ReadUsersSub affects all paths",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanRead(tt.user, tt.meta, tt.path)
			if got != tt.want {
				t.Errorf("CanRead() = %v, want %v\nReason: %s\nUser ID: %v, Meta: %+v, Path: %s",
					got, tt.want, tt.reason, getUserID(tt.user), tt.meta, tt.path)
			}
		})
	}
}

func TestCanWrite(t *testing.T) {
	tests := []struct {
		name   string
		user   *model.User
		meta   *model.Meta
		path   string
		want   bool
		reason string
	}{
		{
			name:   "nil user should deny access",
			user:   nil,
			meta:   nil,
			path:   "/any",
			want:   false,
			reason: "nil user should not have write access",
		},
		{
			name: "nil meta should allow access",
			user: &model.User{
				ID: 1,
			},
			meta:   nil,
			path:   "/any",
			want:   true,
			reason: "nil meta means no restrictions",
		},
		{
			name: "empty WriteUsers list should allow access",
			user: &model.User{
				ID: 1,
			},
			meta: &model.Meta{
				Path:       "/folder",
				WriteUsers: []uint{},
			},
			path:   "/folder",
			want:   true,
			reason: "empty WriteUsers means no user-level restrictions",
		},
		{
			name: "user in WriteUsers list with exact path match",
			user: &model.User{
				ID: 1,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 2, 3},
				WriteUsersSub: false,
			},
			path:   "/folder",
			want:   true,
			reason: "user ID 1 is in WriteUsers list",
		},
		{
			name: "user not in WriteUsers list with exact path match",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 2, 3},
				WriteUsersSub: false,
			},
			path:   "/folder",
			want:   false,
			reason: "user ID 5 is not in WriteUsers list and path matches",
		},
		{
			name: "user not in WriteUsers list with WriteUsersSub=true for sub path",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 2, 3},
				WriteUsersSub: true,
			},
			path:   "/folder/subfolder",
			want:   false,
			reason: "user ID 5 is not in WriteUsers list and WriteUsersSub applies to sub paths",
		},
		{
			name: "user not in WriteUsers list with WriteUsersSub=false for sub path",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 2, 3},
				WriteUsersSub: false,
			},
			path:   "/folder/subfolder",
			want:   true,
			reason: "WriteUsersSub=false means restriction doesn't apply to sub paths",
		},
		{
			name: "user in WriteUsers list with WriteUsersSub=true for sub path",
			user: &model.User{
				ID: 2,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 2, 3},
				WriteUsersSub: true,
			},
			path:   "/folder/subfolder/deep",
			want:   true,
			reason: "user ID 2 is in WriteUsers list so can write to sub paths",
		},
		{
			name: "user not in WriteUsers list for different path",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 2, 3},
				WriteUsersSub: false,
			},
			path:   "/other",
			want:   true,
			reason: "meta path doesn't match request path, so restriction doesn't apply",
		},
		{
			name: "multiple users with mixed permissions",
			user: &model.User{
				ID: 10,
			},
			meta: &model.Meta{
				Path:          "/folder",
				WriteUsers:    []uint{1, 5, 10, 15},
				WriteUsersSub: true,
			},
			path:   "/folder/file.txt",
			want:   true,
			reason: "user ID 10 is in WriteUsers list",
		},
		{
			name: "write restriction at root level",
			user: &model.User{
				ID: 5,
			},
			meta: &model.Meta{
				Path:          "/",
				WriteUsers:    []uint{1},
				WriteUsersSub: true,
			},
			path:   "/any/path",
			want:   false,
			reason: "only user ID 1 can write when root has WriteUsers restriction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanWrite(tt.user, tt.meta, tt.path)
			if got != tt.want {
				t.Errorf("CanWrite() = %v, want %v\nReason: %s\nUser ID: %v, Meta: %+v, Path: %s",
					got, tt.want, tt.reason, getUserID(tt.user), tt.meta, tt.path)
			}
		})
	}
}

func TestCanAccessWithReadPermissions(t *testing.T) {
	tests := []struct {
		name     string
		user     *model.User
		meta     *model.Meta
		reqPath  string
		password string
		want     bool
		reason   string
	}{
		{
			name: "user with read permission and correct password",
			user: &model.User{
				ID:         1,
				Role:       model.GENERAL,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2},
				ReadUsersSub: true,
				Password:     "secret",
				PSub:         true,
			},
			reqPath:  "/folder/file.txt",
			password: "secret",
			want:     true,
			reason:   "user in ReadUsers list with correct password",
		},
		{
			name: "user without read permission even with correct password",
			user: &model.User{
				ID:         5,
				Role:       model.GENERAL,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2},
				ReadUsersSub: true,
				Password:     "secret",
				PSub:         true,
			},
			reqPath:  "/folder/file.txt",
			password: "secret",
			want:     false,
			reason:   "user not in ReadUsers list, should be denied before password check",
		},
		{
			name: "user with read permission but wrong password",
			user: &model.User{
				ID:         1,
				Role:       model.GENERAL,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2},
				ReadUsersSub: true,
				Password:     "secret",
				PSub:         true,
			},
			reqPath:  "/folder/file.txt",
			password: "wrong",
			want:     false,
			reason:   "user in ReadUsers list but wrong password",
		},
		{
			name: "user without read permission and no password",
			user: &model.User{
				ID:         5,
				Role:       model.GENERAL,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:         "/folder",
				ReadUsers:    []uint{1, 2},
				ReadUsersSub: true,
			},
			reqPath:  "/folder/file.txt",
			password: "",
			want:     false,
			reason:   "user not in ReadUsers list should be denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanAccess(tt.user, tt.meta, tt.reqPath, tt.password)
			if got != tt.want {
				t.Errorf("CanAccess() = %v, want %v\nReason: %s",
					got, tt.want, tt.reason)
			}
		})
	}
}

// Helper function to safely get user ID
func getUserID(user *model.User) uint {
	if user == nil {
		return 0
	}
	return user.ID
}

// TestWritePermissionCombinations tests the combined permission check logic
// that is actually used in the codebase:
//
//	if !user.CanWriteContent() && !CanWriteContentBypassUserPerms(meta, path) {
//	    deny
//	}
//	if !CanWrite(user, meta, path) {
//	    deny
//	}
//
// This ensures the three-layer permission system works correctly:
// 1. User-level global write permission (CanWriteContent)
// 2. Meta-level global write permission (CanWriteContentBypassUserPerms)
// 3. Meta-level user whitelist (CanWrite)
func TestWritePermissionCombinations(t *testing.T) {
	tests := []struct {
		name               string
		user               *model.User
		meta               *model.Meta
		path               string
		want               bool
		reason             string
		checkFirstLayer    bool // whether first layer should pass
		checkSecondLayer   bool // whether second layer should pass
		expectedDenyReason string
	}{
		// === Scenario 1: User has global write permission ===
		{
			name: "user has CanWriteContent + in WriteUsers whitelist",
			user: &model.User{
				ID:         1,
				Permission: 1 << 3, // CanWriteContent = true
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         false,
				WriteUsers:    []uint{1},
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               true,
			reason:             "user has global write permission AND is in whitelist",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},
		{
			name: "user has CanWriteContent but NOT in WriteUsers whitelist",
			user: &model.User{
				ID:         1,
				Permission: 1 << 3, // CanWriteContent = true
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         false,
				WriteUsers:    []uint{2, 3}, // user 1 not in list
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               false,
			reason:             "even with global write permission, must pass whitelist check",
			checkFirstLayer:    true,
			checkSecondLayer:   false,
			expectedDenyReason: "whitelist check failed",
		},

		// === Scenario 2: User lacks global permission but meta.Write=true ===
		{
			name: "no CanWriteContent + meta.Write=true + in WriteUsers",
			user: &model.User{
				ID:         1,
				Permission: 0, // CanWriteContent = false
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         true, // bypass enabled
				WSub:          false,
				WriteUsers:    []uint{1},
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               true,
			reason:             "meta.Write bypasses user permission check, and user is in whitelist",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},
		{
			name: "no CanWriteContent + meta.Write=true + NOT in WriteUsers (KEY TEST)",
			user: &model.User{
				ID:         5,
				Permission: 0, // CanWriteContent = false
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         true, // bypass enabled
				WSub:          false,
				WriteUsers:    []uint{1, 2, 3}, // user 5 not in list
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               false,
			reason:             "CRITICAL: meta.Write cannot bypass whitelist check (new behavior)",
			checkFirstLayer:    true,
			checkSecondLayer:   false,
			expectedDenyReason: "whitelist check failed even with meta.Write=true",
		},

		// === Scenario 3: Both checks fail ===
		{
			name: "no CanWriteContent + meta.Write=false",
			user: &model.User{
				ID:         1,
				Permission: 0, // CanWriteContent = false
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         false, // no bypass
				WriteUsers:    []uint{1},
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               false,
			reason:             "denied at first layer: no global permission and no bypass",
			checkFirstLayer:    false,
			checkSecondLayer:   false,
			expectedDenyReason: "first layer check failed",
		},

		// === Scenario 4: Empty WriteUsers (no whitelist restriction) ===
		{
			name: "user has CanWriteContent + empty WriteUsers",
			user: &model.User{
				ID:         1,
				Permission: 1 << 3, // CanWriteContent = true
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         false,
				WriteUsers:    []uint{}, // empty = no restriction
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               true,
			reason:             "empty WriteUsers means no whitelist restriction",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},
		{
			name: "no CanWriteContent + meta.Write=true + empty WriteUsers",
			user: &model.User{
				ID:         1,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         true,
				WSub:          false,
				WriteUsers:    []uint{}, // empty = no restriction
				WriteUsersSub: false,
			},
			path:               "/folder",
			want:               true,
			reason:             "meta.Write bypasses first check, empty whitelist passes second",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},

		// === Scenario 5: Nil meta (no restrictions) ===
		{
			name: "user has CanWriteContent + nil meta",
			user: &model.User{
				ID:         1,
				Permission: 1 << 3,
			},
			meta:               nil,
			path:               "/folder",
			want:               true,
			reason:             "nil meta means no restrictions",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},
		{
			name: "no CanWriteContent + nil meta",
			user: &model.User{
				ID:         1,
				Permission: 0,
			},
			meta:               nil,
			path:               "/folder",
			want:               false,
			reason:             "nil meta cannot bypass lack of user permission",
			checkFirstLayer:    false,
			checkSecondLayer:   true, // would pass if first layer passed
			expectedDenyReason: "first layer check failed",
		},

		// === Scenario 6: Sub-directory inheritance ===
		{
			name: "meta.Write with WSub=true for subdirectory",
			user: &model.User{
				ID:         1,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         true,
				WSub:          true, // applies to subdirectories
				WriteUsers:    []uint{1},
				WriteUsersSub: true,
			},
			path:               "/folder/subfolder",
			want:               true,
			reason:             "WSub=true applies meta.Write to subdirectories",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},
		{
			name: "meta.Write with WSub=false for subdirectory",
			user: &model.User{
				ID:         1,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         true,
				WSub:          false, // does NOT apply to subdirectories
				WriteUsers:    []uint{1},
				WriteUsersSub: false,
			},
			path:               "/folder/subfolder",
			want:               false,
			reason:             "WSub=false means meta.Write doesn't apply to subdirectories",
			checkFirstLayer:    false,
			checkSecondLayer:   true,
			expectedDenyReason: "first layer check failed (WSub=false)",
		},
		{
			name: "WriteUsersSub=false for subdirectory bypasses whitelist",
			user: &model.User{
				ID:         5, // not in WriteUsers
				Permission: 1 << 3,
			},
			meta: &model.Meta{
				Path:          "/folder",
				Write:         false,
				WriteUsers:    []uint{1, 2},
				WriteUsersSub: false, // whitelist does NOT apply to subdirectories
			},
			path:               "/folder/subfolder",
			want:               true,
			reason:             "WriteUsersSub=false means whitelist doesn't apply to subdirectories",
			checkFirstLayer:    true,
			checkSecondLayer:   true, // passes because restriction doesn't apply
			expectedDenyReason: "",
		},

		// === Scenario 7: Root level restriction ===
		{
			name: "root level meta.Write with user in whitelist",
			user: &model.User{
				ID:         1,
				Permission: 0,
			},
			meta: &model.Meta{
				Path:          "/",
				Write:         true,
				WSub:          true,
				WriteUsers:    []uint{1},
				WriteUsersSub: true,
			},
			path:               "/any/deep/path",
			want:               true,
			reason:             "root level permissions apply to all paths",
			checkFirstLayer:    true,
			checkSecondLayer:   true,
			expectedDenyReason: "",
		},
		{
			name: "root level restriction denies non-whitelisted user",
			user: &model.User{
				ID:         5,
				Permission: 1 << 3, // has global permission
			},
			meta: &model.Meta{
				Path:          "/",
				Write:         false,
				WriteUsers:    []uint{1, 2},
				WriteUsersSub: true,
			},
			path:               "/any/path",
			want:               false,
			reason:             "root level whitelist restricts all paths",
			checkFirstLayer:    true,
			checkSecondLayer:   false,
			expectedDenyReason: "not in root level whitelist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the actual permission check logic
			firstLayerPass := tt.user.CanWriteContent() || CanWriteContentBypassUserPerms(tt.meta, tt.path)
			secondLayerPass := CanWrite(tt.user, tt.meta, tt.path)

			// Verify our understanding of each layer
			if firstLayerPass != tt.checkFirstLayer {
				t.Errorf("First layer check mismatch: got %v, expected %v\n"+
					"CanWriteContent()=%v, CanWriteContentBypassUserPerms()=%v",
					firstLayerPass, tt.checkFirstLayer,
					tt.user.CanWriteContent(), CanWriteContentBypassUserPerms(tt.meta, tt.path))
			}

			if firstLayerPass && secondLayerPass != tt.checkSecondLayer {
				t.Errorf("Second layer check mismatch: got %v, expected %v\n"+
					"CanWrite()=%v",
					secondLayerPass, tt.checkSecondLayer,
					CanWrite(tt.user, tt.meta, tt.path))
			}

			// Final result
			got := firstLayerPass && secondLayerPass

			if got != tt.want {
				t.Errorf("Permission check failed:\n"+
					"  Result: %v, want %v\n"+
					"  Reason: %s\n"+
					"  First layer (CanWriteContent || CanWriteContentBypassUserPerms): %v\n"+
					"  Second layer (CanWrite): %v\n"+
					"  User: ID=%d, Permission=%d, CanWriteContent=%v\n"+
					"  Meta: Path=%s, Write=%v, WSub=%v, WriteUsers=%v, WriteUsersSub=%v\n"+
					"  Check Path: %s",
					got, tt.want,
					tt.reason,
					firstLayerPass,
					secondLayerPass,
					tt.user.ID, tt.user.Permission, tt.user.CanWriteContent(),
					getMetaPath(tt.meta), getMetaWrite(tt.meta), getMetaWSub(tt.meta),
					getMetaWriteUsers(tt.meta), getMetaWriteUsersSub(tt.meta),
					tt.path)
			}
		})
	}
}

// Helper functions to safely extract meta fields
func getMetaPath(meta *model.Meta) string {
	if meta == nil {
		return "nil"
	}
	return meta.Path
}

func getMetaWrite(meta *model.Meta) bool {
	if meta == nil {
		return false
	}
	return meta.Write
}

func getMetaWSub(meta *model.Meta) bool {
	if meta == nil {
		return false
	}
	return meta.WSub
}

func getMetaWriteUsers(meta *model.Meta) []uint {
	if meta == nil {
		return nil
	}
	return meta.WriteUsers
}

func getMetaWriteUsersSub(meta *model.Meta) bool {
	if meta == nil {
		return false
	}
	return meta.WriteUsersSub
}
