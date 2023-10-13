package go_librespot

import "io"

type SizedReadAtSeeker interface {
	io.ReadSeeker
	io.ReaderAt

	Size() int64
}
