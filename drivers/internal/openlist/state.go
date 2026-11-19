package openlist

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

// State is the product-specific configuration used by the shared AList/OpenList
// protocol implementation. Pointers keep login and address normalization updates
// on the registered driver value rather than on a copied adapter.
type State struct {
	Driver driver.Driver

	Address      *string
	MetaPassword *string
	Username     *string
	Password     *string
	Token        *string

	PassIP          bool
	PassUserAgent   bool
	ForwardArchives bool
	ForwardRefresh  bool
	SendOverwrite   bool
}
