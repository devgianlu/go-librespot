package go_librespot

import "context"

// GetLogin5TokenFunc is a function that everytime it is called returns a valid login5 access token.
type GetLogin5TokenFunc func(ctx context.Context, force bool) (string, error)
