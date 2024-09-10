package ap

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

func writeMessage(w io.Writer, withHello bool, m proto.Message) error {
	// marshal message
	data, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed marshalling message: %w", err)
	}

	var helloLen int
	if withHello {
		// write 0x00, 0x04
		if _, err := w.Write([]byte{0, 4}); err != nil {
			return fmt.Errorf("failed writing hello bytes: %w", err)
		}

		helloLen = 2
	}

	// write length
	if err := binary.Write(w, binary.BigEndian, uint32(helloLen+4+len(data))); err != nil {
		return fmt.Errorf("failed writing message length: %w", err)
	}

	// write message
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("faield writing message: %w", err)
	}

	return nil
}

func readMessage(r io.Reader, m proto.Message) error {
	// read length
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return fmt.Errorf("failed reading message length: %w", err)
	}

	// read message
	data := make([]byte, length-4)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("failed reading message body: %w", err)
	}

	// unmarshal message
	if err := proto.Unmarshal(data, m); err != nil {
		return fmt.Errorf("failed unmarshalling message: %w", err)
	}

	return nil
}
