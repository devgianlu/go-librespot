package go_librespot

// GetLogin5TokenFunc is a function that everytime it is called returns a valid login5 access token.
type GetLogin5TokenFunc func() (string, error)
