package vorbis

import (
	"errors"
	"fmt"

	"github.com/xlab/vorbis-go/vorbis"
)

// ReadInfo reads info and comment into Info, a go-friendly struct.
func ReadInfo(vi *vorbis.Info, vc *vorbis.Comment) Info {
	info := Info{
		Channels:   vi.Channels,
		SampleRate: int32(vi.Rate),
		Vendor:     toString(vc.Vendor, 256),
	}
	lengths := vc.CommentLengths[:vc.Comments]
	userComments := vc.UserComments[:vc.Comments]
	for i, text := range userComments {
		info.Comments = append(info.Comments, string(text[:lengths[i]]))
	}
	return info
}

// ReadHeaders allows to init info and comment from a private codec data payload
// when no Ogg stream is available. Info and comment must be initialised beforehand.
func ReadHeaders(codecPriv []byte, info *vorbis.Info, comment *vorbis.Comment) error {
	if len(codecPriv) == 0 {
		return errors.New("vorbis decoder: no codec private data")
	}
	headerSizes := make([]int, 3)
	headers := make([][]byte, 3)
	p := codecPriv

	if p[0] == 0x00 && p[1] == 0x30 {
		for i := 0; i < 3; i++ {
			headerSizes[i] = int(uint16(p[0])<<8 | uint16(p[1]))
			headers[i] = p
			p = p[headerSizes[i]:]
		}
	} else if p[0] == 0x02 {
		offset := 1
		p = p[1:]
		for i := 0; i < 2; i++ {
			headerSizes[i] = 0
			for (p[0] == 0xFF) && offset < len(codecPriv) {
				headerSizes[i] += 0xFF
				offset++
				p = p[1:]
			}
			if offset >= len(codecPriv)-1 {
				return errors.New("vorbis decoder: header sizes damaged")
			}
			headerSizes[i] += int(p[0])
			offset++
			p = p[1:]
		}
		headerSizes[2] = len(codecPriv) - headerSizes[0] - headerSizes[1] - offset
		headers[0] = codecPriv[offset:]
		headers[1] = codecPriv[offset+headerSizes[0]:]
		headers[2] = codecPriv[offset+headerSizes[0]+headerSizes[1]:]
	} else {
		return fmt.Errorf("vorbis decoder: initial header len is wrong: %d", p[0])
	}
	for i := 0; i < 3; i++ {
		packet := vorbis.OggPacket{
			BOS:    b(i == 0),
			Bytes:  headerSizes[i],
			Packet: headers[i],
		}
		if ret := vorbis.SynthesisHeaderin(info, comment, &packet); ret < 0 {
			return fmt.Errorf("vorbis decoder: %d. header damaged", i+1)
		}
	}
	return nil
}

func b(v bool) int {
	if v {
		return 1
	}
	return 0
}

func toString(buf []byte, maxlen int) string {
	buf = buf[:maxlen]
	for i := range buf {
		if buf[i] == 0 {
			return string(buf[:i:i])
		}
	}
	return ""
}
