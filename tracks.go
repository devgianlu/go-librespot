package go_librespot

type PageResolver[T any] interface {
	Page(idx int) ([]T, error)
}
