package go_librespot

import "context"

type PageResolver[T any] interface {
	Page(ctx context.Context, idx int) ([]T, error)
}
