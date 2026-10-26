package message

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/thtpark/fdn/internal/message/wire"
	"google.golang.org/protobuf/proto"
)

func readEnvelope(conn net.Conn) (*wire.Envelope, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:1]); err != nil {
		return nil, unavailable("message connection closed")
	}
	if err := conn.SetReadDeadline(time.Now().Add(FrameReadTimeout)); err != nil {
		return nil, unavailable("message read deadline unavailable")
	}
	defer conn.SetReadDeadline(time.Time{})
	if _, err := io.ReadFull(conn, header[1:]); err != nil {
		return nil, invalid("truncated message frame")
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return nil, invalid("invalid message frame length")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(conn, data); err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, unavailable("message frame read timed out")
		}
		return nil, invalid("truncated message frame")
	}
	envelope := new(wire.Envelope)
	if err := validateProtobuf(data, envelope.ProtoReflect().Descriptor()); err != nil {
		return nil, err
	}
	if err := (proto.UnmarshalOptions{RecursionLimit: MaxPayloadDepth + 1}).Unmarshal(data, envelope); err != nil || envelope.Body == nil {
		return nil, invalid("invalid message envelope")
	}
	return envelope, nil
}

func writeEnvelope(conn net.Conn, envelope *wire.Envelope, timeout time.Duration) error {
	size := proto.Size(envelope)
	if size == 0 || size > MaxFrameBytes {
		return invalid("invalid message frame length")
	}
	data := make([]byte, 4, size+4)
	binary.BigEndian.PutUint32(data, uint32(size))
	data, err := (proto.MarshalOptions{Deterministic: true}).MarshalAppend(data, envelope)
	if err != nil {
		return invalid("invalid message envelope")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return unavailable("message write deadline unavailable")
	}
	defer conn.SetWriteDeadline(time.Time{})
	for len(data) != 0 {
		n, err := conn.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil || n == 0 {
			return unavailable("message write failed")
		}
	}
	return nil
}
