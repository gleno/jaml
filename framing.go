package jaml

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	frameHeader     = 8
	controlFrameCap = 1 << 10
	greetingCap     = 1 << 10
)

type _FrameReader struct {
	source  io.Reader
	limit   int
	refusal error
	quiet   *_Quiet
}

type _Metered struct {
	source io.Reader
	quiet  *_Quiet
}

type _Borrowed []byte

func (b _Borrowed) MarshalJSON() ([]byte, error) {
	if b == nil {
		return []byte("null"), nil
	}
	return b, nil
}

func (b *_Borrowed) UnmarshalJSON(written []byte) error {
	*b = written
	return nil
}

func (m _Metered) Read(into []byte) (int, error) {
	m.quiet.waiting()
	defer m.quiet.heard()
	return m.source.Read(into)
}

func (r *_FrameReader) size() (int, error) {
	var header [frameHeader]byte
	if _, err := io.ReadFull(_Metered{source: r.source, quiet: r.quiet}, header[:]); err != nil {
		return 0, ended(err)
	}
	var size = binary.BigEndian.Uint64(header[:])
	if size > uint64(r.limit) {
		return 0, r.refusal
	}
	return int(size), nil
}

func (r *_FrameReader) body(size int) ([]byte, error) {
	var frame = make([]byte, size)
	if _, err := io.ReadFull(_Metered{source: r.source, quiet: r.quiet}, frame); err != nil {
		return nil, ended(err)
	}
	return frame, nil
}

func (r *_FrameReader) next() ([]byte, error) {
	size, err := r.size()
	if err != nil {
		return nil, err
	}
	return r.body(size)
}

func (r *_FrameReader) into(size int, target any) error {
	frame, err := r.body(size)
	if err != nil {
		return err
	}
	return json.Unmarshal(frame, target)
}

func ended(err error) error {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, os.ErrClosed):
		return errHungUp
	}
	return err
}

func headed(frame []byte) []byte {
	var whole = make([]byte, frameHeader, frameHeader+len(frame))
	binary.BigEndian.PutUint64(whole, uint64(len(frame)))
	return append(whole, frame...)
}

func header(size int) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(size))
}

func oversizedControl() error {
	return fmt.Errorf("a control message larger than %d bytes", controlFrameCap)
}
