package handles

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func accessContext(rawQuery string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/admin/file_access/list?"+rawQuery, nil)
	return c
}

func TestAccessPaging(t *testing.T) {
	limit, offset, err := accessPaging(accessContext(""))
	require.NoError(t, err)
	assert.Equal(t, accessDefaultLimit, limit)
	assert.Equal(t, 0, offset)

	limit, offset, err = accessPaging(accessContext("limit=50&offset=100"))
	require.NoError(t, err)
	assert.Equal(t, 50, limit)
	assert.Equal(t, 100, offset)

	// Above the ceiling is clamped, not an error: the caller still gets a page.
	limit, _, err = accessPaging(accessContext("limit=999999"))
	require.NoError(t, err)
	assert.Equal(t, accessMaxLimit, limit)

	// Invalid values are client errors instead of a silent fallback that
	// answered a different row set than asked for.
	for _, query := range []string{"limit=0", "limit=-1", "limit=abc", "offset=-5", "offset=x"} {
		_, _, err = accessPaging(accessContext(query))
		assert.Error(t, err, query)
	}
}

func TestAccessPrefixConditionUsesBinaryOnMySQLOnly(t *testing.T) {
	clause, args := accessPrefixCondition("sqlite", "path", "/Movies/动作")
	assert.Equal(t, "substr(path, 1, ?) = ?", clause)
	assert.Equal(t, []any{10, "/Movies/动作"}, args, "the length counts runes, not bytes")

	// MySQL compares with the column collation, which is case-insensitive by
	// default; BINARY is what makes the prefix test case-sensitive there.
	clause, _ = accessPrefixCondition("mysql", "path", "/Movies")
	assert.Equal(t, "BINARY substr(path, 1, ?) = ?", clause)

	clause, _ = accessPrefixCondition("postgres", "path", "/Movies")
	assert.Equal(t, "substr(path, 1, ?) = ?", clause)
}

func TestAccessBoolFlag(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1", "yes", "on"} {
		got, err := accessBoolFlag(raw)
		require.NoError(t, err, raw)
		assert.True(t, got, raw)
	}
	for _, raw := range []string{"", "false", "0", "no", "off"} {
		got, err := accessBoolFlag(raw)
		require.NoError(t, err, raw)
		assert.False(t, got, raw)
	}
	_, err := accessBoolFlag("maybe")
	assert.Error(t, err, "an unrecognised value must not silently mean false")
}
