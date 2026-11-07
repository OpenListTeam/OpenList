package s3

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestV1MarkerEchoIsUrlEncoded pins the v1 <Marker> echo of an
// encoding-type=url listing.
//
// gofakes3 encodes <Key>/<Prefix>/<Delimiter> but copies the request's marker
// verbatim, so a client that decodes the whole document (which is what asking
// for encoding-type=url means) got a different marker back than the one it
// sent: a marker holding a literal "%20" decoded a second time into a space and
// the next page was requested from a key that does not exist. aws-cli and
// rclone both resend the echoed marker, so the listing silently skipped keys.
func TestV1MarkerEchoIsUrlEncoded(t *testing.T) {
	srv, root := auditServer(t)
	auditTree(t, srv, root, "a%20b.txt", "a b.txt", "c.txt")

	marker := "a%20b.txt"
	page := auditGet(t, srv, "encoding-type=url&marker="+url.QueryEscape(marker))

	decoded, err := url.QueryUnescape(page.Marker)
	require.NoError(t, err)
	assert.Equal(t, marker, decoded,
		"the echoed marker must survive one round of the decoding the client applies")
	assert.Contains(t, page.Marker, "%2520",
		"the literal percent of the marker must be encoded, not passed through")

	// The same marker must still page the way the unencoded listing does.
	plain := auditGet(t, srv, "marker="+url.QueryEscape(marker))
	encodedKeys, _ := page.decode(true)
	plainKeys, _ := plain.decode(false)
	assert.Equal(t, plainKeys, encodedKeys,
		"encoded and plain listings must report the same keys for the same marker")
}
