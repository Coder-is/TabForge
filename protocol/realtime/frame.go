// Package realtime serves and consumes contract channels over WebSocket.
//
// A channel connection carries numbered frames in both directions. Each side
// keeps what it sent until the peer acknowledges it, so a session survives
// reconnects: after resuming, unacknowledged frames are sent again and the
// receiver drops the duplicates. Requests therefore run at most once per
// session and responses and pushes are delivered once, in order. See
// doc/realtime.md for the wire format.
package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/encoding/protowire"
)

// Subprotocols negotiated in the WebSocket handshake.
const (
	SubprotocolProto = "tabforge.realtime.v1.proto"
	SubprotocolJSON  = "tabforge.realtime.v1.json"
)

type Kind uint32

const (
	KindHello    Kind = 1
	KindWelcome  Kind = 2
	KindRequest  Kind = 3
	KindResponse Kind = 4
	KindPush     Kind = 5
	KindNotify   Kind = 6
	KindPing     Kind = 7
	KindPong     Kind = 8
	KindAck      Kind = 9
	KindClose    Kind = 10
)

var kindNames = map[Kind]string{KindHello: "hello", KindWelcome: "welcome", KindRequest: "request", KindResponse: "response", KindPush: "push", KindNotify: "notify", KindPing: "ping", KindPong: "pong", KindAck: "ack", KindClose: "close"}

func (k Kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "kind(" + strconv.FormatUint(uint64(k), 10) + ")"
}

// Error is a transport or application failure carried by response and close frames.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Transport error codes. Applications may return any other code from handlers.
const (
	CodeBadRequest      = "bad_request"
	CodeUnauthorized    = "unauthorized"
	CodeVersionMismatch = "version_mismatch"
	CodeSchemaMismatch  = "schema_mismatch"
	CodeSessionExpired  = "session_expired"
	CodeSessionLost     = "session_lost"
	CodeProtocolError   = "protocol_error"
	CodeOverloaded      = "overloaded"
	CodeRateLimited     = "rate_limited"
	CodeTimeout         = "timeout"
	CodeInternal        = "internal"
	CodeNotImplemented  = "not_implemented"
	CodeKicked          = "kicked"
	CodeDuplicateLogin  = "duplicate_login"
	CodeServerShutdown  = "server_shutdown"
	CodeClosed          = "closed"
	CodeCancelled       = "cancelled"
)

type Hello struct {
	ProtocolVersion string `json:"protocolVersion"`
	SchemaHash      string `json:"schemaHash,omitempty"`
	Token           string `json:"token,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	ResumeToken     string `json:"resumeToken,omitempty"`
}

type Welcome struct {
	SessionID      string `json:"sessionId"`
	ResumeToken    string `json:"resumeToken"`
	Resumed        bool   `json:"resumed,omitempty"`
	HeartbeatMS    uint32 `json:"heartbeatMS"`
	ResumeWindowMS uint32 `json:"resumeWindowMS,omitempty"`
}

// Frame is one WebSocket message. Payload holds binary Protobuf for the proto
// subprotocol and a ProtoJSON object for the JSON subprotocol.
type Frame struct {
	Kind    Kind
	Seq     uint64
	Ack     uint64
	Message uint32
	ReplyTo uint64
	Payload []byte
	Error   *Error
	Hello   *Hello
	Welcome *Welcome
}

// Reliable frames take part in sequencing; volatile push/notify have Seq 0.
func (f *Frame) reliable() bool { return f.Seq != 0 }

type Encoding int

const (
	EncodingProto Encoding = iota
	EncodingJSON
)

func (e Encoding) Subprotocol() string {
	if e == EncodingJSON {
		return SubprotocolJSON
	}
	return SubprotocolProto
}

func encodingFor(subprotocol string) (Encoding, bool) {
	switch subprotocol {
	case SubprotocolProto:
		return EncodingProto, true
	case SubprotocolJSON:
		return EncodingJSON, true
	}
	return 0, false
}

// Field numbers of the frame envelope (see doc/realtime.md).
const (
	fieldKind    = 1
	fieldSeq     = 2
	fieldAck     = 3
	fieldMessage = 4
	fieldReplyTo = 5
	fieldPayload = 6
	fieldError   = 7
	fieldHello   = 8
	fieldWelcome = 9
)

func appendString(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendVarint(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendBool(b []byte, num protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	return appendVarint(b, num, 1)
}

func appendMessage(b []byte, num protowire.Number, body []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, body)
}

// Encode serialises a frame. It does not validate; senders build valid frames.
func (f *Frame) Encode(enc Encoding) ([]byte, error) {
	if enc == EncodingJSON {
		return f.encodeJSON()
	}
	var b []byte
	b = appendVarint(b, fieldKind, uint64(f.Kind))
	b = appendVarint(b, fieldSeq, f.Seq)
	b = appendVarint(b, fieldAck, f.Ack)
	b = appendVarint(b, fieldMessage, uint64(f.Message))
	b = appendVarint(b, fieldReplyTo, f.ReplyTo)
	if len(f.Payload) != 0 {
		b = protowire.AppendTag(b, fieldPayload, protowire.BytesType)
		b = protowire.AppendBytes(b, f.Payload)
	}
	if e := f.Error; e != nil {
		var body []byte
		body = appendString(body, 1, e.Code)
		body = appendString(body, 2, e.Message)
		body = appendBool(body, 3, e.Retryable)
		b = appendMessage(b, fieldError, body)
	}
	if h := f.Hello; h != nil {
		var body []byte
		body = appendString(body, 1, h.ProtocolVersion)
		body = appendString(body, 2, h.SchemaHash)
		body = appendString(body, 3, h.Token)
		body = appendString(body, 4, h.SessionID)
		body = appendString(body, 5, h.ResumeToken)
		b = appendMessage(b, fieldHello, body)
	}
	if w := f.Welcome; w != nil {
		var body []byte
		body = appendString(body, 1, w.SessionID)
		body = appendString(body, 2, w.ResumeToken)
		body = appendBool(body, 3, w.Resumed)
		body = appendVarint(body, 4, uint64(w.HeartbeatMS))
		body = appendVarint(body, 5, uint64(w.ResumeWindowMS))
		b = appendMessage(b, fieldWelcome, body)
	}
	return b, nil
}

var errMalformed = errors.New("malformed frame")

// walk visits each field of a Protobuf message body. Unknown fields are
// skipped so newer peers can add envelope fields.
func walk(data []byte, visit func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error) error {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 || num < 1 {
			return errMalformed
		}
		data = data[n:]
		var value []byte
		var varint uint64
		switch typ {
		case protowire.VarintType:
			varint, n = protowire.ConsumeVarint(data)
		case protowire.BytesType:
			value, n = protowire.ConsumeBytes(data)
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
		}
		if n < 0 {
			return errMalformed
		}
		data = data[n:]
		if err := visit(num, typ, value, varint); err != nil {
			return err
		}
	}
	return nil
}

func expect(typ, want protowire.Type) error {
	if typ != want {
		return errMalformed
	}
	return nil
}

func decodeStrings(data []byte, fields ...*string) error {
	return walk(data, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
		if int(num) <= len(fields) {
			if err := expect(typ, protowire.BytesType); err != nil {
				return err
			}
			*fields[num-1] = string(value)
		}
		return nil
	})
}

// DecodeFrame parses and validates one WebSocket message.
func DecodeFrame(enc Encoding, data []byte) (*Frame, error) {
	var f *Frame
	var err error
	if enc == EncodingJSON {
		f, err = decodeJSON(data)
	} else {
		f, err = decodeProto(data)
	}
	if err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return f, nil
}

func decodeProto(data []byte) (*Frame, error) {
	f := &Frame{}
	err := walk(data, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch num {
		case fieldKind, fieldSeq, fieldAck, fieldMessage, fieldReplyTo:
			if err := expect(typ, protowire.VarintType); err != nil {
				return err
			}
			switch num {
			case fieldKind:
				if varint > 1<<31 {
					return errMalformed
				}
				f.Kind = Kind(varint)
			case fieldSeq:
				f.Seq = varint
			case fieldAck:
				f.Ack = varint
			case fieldMessage:
				if varint > protocol.MaxMessageID {
					return errMalformed
				}
				f.Message = uint32(varint)
			case fieldReplyTo:
				f.ReplyTo = varint
			}
		case fieldPayload:
			if err := expect(typ, protowire.BytesType); err != nil {
				return err
			}
			f.Payload = append([]byte(nil), value...)
		case fieldError:
			if err := expect(typ, protowire.BytesType); err != nil {
				return err
			}
			e := &Error{}
			err := walk(value, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
				switch num {
				case 1, 2:
					if err := expect(typ, protowire.BytesType); err != nil {
						return err
					}
					if num == 1 {
						e.Code = string(value)
					} else {
						e.Message = string(value)
					}
				case 3:
					if err := expect(typ, protowire.VarintType); err != nil {
						return err
					}
					e.Retryable = varint != 0
				}
				return nil
			})
			if err != nil {
				return err
			}
			f.Error = e
		case fieldHello:
			if err := expect(typ, protowire.BytesType); err != nil {
				return err
			}
			h := &Hello{}
			if err := decodeStrings(value, &h.ProtocolVersion, &h.SchemaHash, &h.Token, &h.SessionID, &h.ResumeToken); err != nil {
				return err
			}
			f.Hello = h
		case fieldWelcome:
			if err := expect(typ, protowire.BytesType); err != nil {
				return err
			}
			w := &Welcome{}
			err := walk(value, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
				switch num {
				case 1, 2:
					if err := expect(typ, protowire.BytesType); err != nil {
						return err
					}
					if num == 1 {
						w.SessionID = string(value)
					} else {
						w.ResumeToken = string(value)
					}
				case 3, 4, 5:
					if err := expect(typ, protowire.VarintType); err != nil {
						return err
					}
					if num == 3 {
						w.Resumed = varint != 0
					} else if varint > 1<<32-1 {
						return errMalformed
					} else if num == 4 {
						w.HeartbeatMS = uint32(varint)
					} else {
						w.ResumeWindowMS = uint32(varint)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			f.Welcome = w
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

type jsonFrame struct {
	Kind    string          `json:"kind"`
	Seq     string          `json:"seq,omitempty"`
	Ack     string          `json:"ack,omitempty"`
	Message uint32          `json:"message,omitempty"`
	ReplyTo string          `json:"replyTo,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	Hello   *Hello          `json:"hello,omitempty"`
	Welcome *Welcome        `json:"welcome,omitempty"`
}

func decimal(v uint64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatUint(v, 10)
}

var canonicalDecimal = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func parseDecimal(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	if !canonicalDecimal.MatchString(s) {
		return 0, errMalformed
	}
	return strconv.ParseUint(s, 10, 64)
}

func (f *Frame) encodeJSON() ([]byte, error) {
	j := jsonFrame{Kind: f.Kind.String(), Seq: decimal(f.Seq), Ack: decimal(f.Ack), Message: f.Message, ReplyTo: decimal(f.ReplyTo), Error: f.Error, Hello: f.Hello, Welcome: f.Welcome}
	if f.Payload != nil {
		j.Payload = f.Payload
	}
	return json.Marshal(j)
}

func decodeJSON(data []byte) (*Frame, error) {
	if err := protocol.ValidateJSON(data); err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformed, err)
	}
	var j jsonFrame
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformed, err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errMalformed
	}
	f := &Frame{Message: j.Message, Error: j.Error, Hello: j.Hello, Welcome: j.Welcome}
	for kind, name := range kindNames {
		if name == j.Kind {
			f.Kind = kind
		}
	}
	var err error
	if f.Seq, err = parseDecimal(j.Seq); err != nil {
		return nil, err
	}
	if f.Ack, err = parseDecimal(j.Ack); err != nil {
		return nil, err
	}
	if f.ReplyTo, err = parseDecimal(j.ReplyTo); err != nil {
		return nil, err
	}
	if j.Payload != nil {
		if trimmed := bytes.TrimSpace(j.Payload); len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("%w: payload must be a JSON object", errMalformed)
		}
		f.Payload = j.Payload
	}
	return f, nil
}

// validate checks the envelope rules shared by every implementation.
func (f *Frame) validate() error {
	bad := func(reason string) error { return fmt.Errorf("%w: %s %s", errMalformed, f.Kind, reason) }
	if f.Message > protocol.MaxMessageID {
		return bad("message id out of range")
	}
	control := f.Seq == 0 && f.Message == 0 && f.ReplyTo == 0 && f.Payload == nil
	switch f.Kind {
	case KindHello:
		if !control || f.Hello == nil || f.Welcome != nil || f.Error != nil || f.Hello.ProtocolVersion == "" || (f.Hello.SessionID == "") != (f.Hello.ResumeToken == "") {
			return bad("needs hello with protocolVersion; sessionId and resumeToken go together")
		}
	case KindWelcome:
		if !control || f.Welcome == nil || f.Hello != nil || f.Error != nil || f.Welcome.SessionID == "" || f.Welcome.ResumeToken == "" || f.Welcome.HeartbeatMS == 0 {
			return bad("needs welcome with sessionId, resumeToken and heartbeatMS")
		}
	case KindRequest, KindPush, KindNotify:
		if f.Message == 0 || f.ReplyTo != 0 || f.Error != nil || f.Hello != nil || f.Welcome != nil {
			return bad("needs a message id and only a payload")
		}
		if f.Kind == KindRequest && f.Seq == 0 {
			return bad("must be sequenced")
		}
	case KindResponse:
		if f.Seq == 0 || f.Message == 0 || f.ReplyTo == 0 || f.Hello != nil || f.Welcome != nil || (f.Error != nil && f.Payload != nil) {
			return bad("needs seq, message, replyTo and payload or error")
		}
	case KindPing, KindPong, KindAck:
		if !control || f.Error != nil || f.Hello != nil || f.Welcome != nil {
			return bad("carries only ack")
		}
	case KindClose:
		if !control || f.Error == nil || f.Hello != nil || f.Welcome != nil {
			return bad("needs an error")
		}
	default:
		return fmt.Errorf("%w: unknown kind %d", errMalformed, f.Kind)
	}
	if f.Error != nil && f.Error.Code == "" {
		return bad("error needs a code")
	}
	return nil
}
