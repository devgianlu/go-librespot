package go_librespot

import "context"

// GetAddressFunc is a function that everytime it is called returns a different address for that type of endpoint.
type GetAddressFunc func(ctx context.Context) string
