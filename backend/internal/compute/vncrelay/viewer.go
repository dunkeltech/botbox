// SPDX-License-Identifier: AGPL-3.0-only

package vncrelay

import (
	"encoding/binary"
	"errors"
	"io"
)

const rfbVersion = "RFB 003.008\n"

var errViewerProtocol = errors.New("unsupported view-only RFB message")

// The sandbox uses RFB 3.8 with security type None. Consume the handshake in
// protocol order so client input cannot be mistaken for initialization.
func viewerHandshake(stream io.Reader, forward func([]byte) error) error {
	for _, expected := range []string{rfbVersion, "\x01", "\x01"} {
		data := make([]byte, len(expected))
		if _, err := io.ReadFull(stream, data); err != nil {
			return err
		}
		if string(data) != expected {
			return errViewerProtocol
		}
		if err := forward(data); err != nil {
			return err
		}
	}
	return nil
}

func forwardViewerMessages(stream io.Reader, forward func([]byte) error) error {
	if err := viewerHandshake(stream, forward); err != nil {
		return err
	}
	for {
		var message [20]byte
		if _, err := io.ReadFull(stream, message[:1]); err != nil {
			return err
		}
		var headerSize, bodySize int
		switch message[0] {
		case 0: // SetPixelFormat
			headerSize = 20
		case 2: // SetEncodings
			headerSize = 4
		case 3, 150: // FramebufferUpdateRequest, EnableContinuousUpdates
			headerSize = 10
		case 248: // ClientFence
			headerSize = 9
		case 4, 5, 6: // Keyboard, pointer and clipboard input
			if err := discardViewerInput(stream, message[0]); err != nil {
				return err
			}
			continue
		default:
			return errViewerProtocol
		}
		if _, err := io.ReadFull(stream, message[1:headerSize]); err != nil {
			return err
		}
		switch message[0] {
		case 2:
			bodySize = 4 * int(binary.BigEndian.Uint16(message[2:4]))
			if bodySize > 4096 {
				return errViewerProtocol
			}
		case 248:
			bodySize = int(message[8])
			if bodySize > 64 {
				return errViewerProtocol
			}
		}
		data := append(make([]byte, 0, headerSize+bodySize), message[:headerSize]...)
		data = data[:headerSize+bodySize]
		if _, err := io.ReadFull(stream, data[headerSize:]); err != nil {
			return err
		}
		if err := forward(data); err != nil {
			return err
		}
	}
}

func discardViewerInput(stream io.Reader, opcode byte) error {
	switch opcode {
	case 4:
		_, err := io.CopyN(io.Discard, stream, 7)
		return err
	case 5:
		_, err := io.CopyN(io.Discard, stream, 5)
		return err
	default:
		var header [7]byte
		if _, err := io.ReadFull(stream, header[:]); err != nil {
			return err
		}
		// Extended clipboard frames encode their size as a negative int32.
		size := int64(int32(binary.BigEndian.Uint32(header[3:]))) //nolint:gosec // RFB length is signed on the wire
		if size < 0 {
			size = -size
		}
		if size > 1<<20 {
			return errViewerProtocol
		}
		_, err := io.CopyN(io.Discard, stream, size)
		return err
	}
}
