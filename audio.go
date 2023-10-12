package go_librespot

import "io"

type SizedReadSeeker interface {
	io.ReadSeeker

	Size() int64
}
