package realtime

import (
	"fmt"
	"sync"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// payloadCodec converts messages for one contract. Generated Go types are
// accepted when their schema equals the contract's; otherwise dynamic
// messages are used.
type payloadCodec struct {
	types   *protoregistry.Types
	checked sync.Map // protoreflect.MessageDescriptor -> error
}

func newPayloadCodec(c *protocol.Contract) *payloadCodec {
	return &payloadCodec{types: c.Types}
}

func (p *payloadCodec) encode(enc Encoding, want protoreflect.MessageDescriptor, m proto.Message) ([]byte, error) {
	if m == nil || !m.ProtoReflect().IsValid() {
		return nil, fmt.Errorf("expected %s, got nil", want.FullName())
	}
	if err := p.check(m.ProtoReflect().Descriptor(), want); err != nil {
		return nil, err
	}
	if enc == EncodingJSON {
		return protojson.MarshalOptions{Resolver: p.types}.Marshal(m)
	}
	data, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func (p *payloadCodec) decode(enc Encoding, data []byte, into proto.Message) error {
	if enc == EncodingJSON {
		if data == nil {
			data = []byte("{}")
		}
		if err := protocol.ValidateJSON(data); err != nil {
			return err
		}
		return protojson.UnmarshalOptions{Resolver: p.types}.Unmarshal(data, into)
	}
	return proto.Unmarshal(data, into)
}

// check accepts a message whose descriptor is, or is structurally equal to,
// the contract descriptor. A generated type built from a different schema
// with the same name is rejected rather than silently mis-encoded.
func (p *payloadCodec) check(got, want protoreflect.MessageDescriptor) error {
	if got == want {
		return nil
	}
	if cached, ok := p.checked.Load(got); ok {
		if cached == nil {
			return nil
		}
		return cached.(error)
	}
	var err error
	if !sameSchema(got, want, map[protoreflect.FullName]bool{}) {
		err = fmt.Errorf("message %s does not match the contract schema", got.FullName())
	}
	if err == nil {
		p.checked.Store(got, nil)
	} else {
		p.checked.Store(got, err)
	}
	return err
}

func sameSchema(a, b protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) bool {
	if a.FullName() != b.FullName() {
		return false
	}
	if seen[a.FullName()] {
		return true
	}
	seen[a.FullName()] = true
	if !proto.Equal(protodesc.ToDescriptorProto(a), protodesc.ToDescriptorProto(b)) {
		return false
	}
	for i := 0; i < a.Fields().Len(); i++ {
		fa, fb := a.Fields().Get(i), b.Fields().Get(i)
		if fa.Message() != nil && !sameSchema(fa.Message(), fb.Message(), seen) {
			return false
		}
		if fa.Enum() != nil && !proto.Equal(protodesc.ToEnumDescriptorProto(fa.Enum()), protodesc.ToEnumDescriptorProto(fb.Enum())) {
			return false
		}
	}
	return true
}

// factory returns a constructor for messages of desc, preferring the
// registered Go type for T when it matches the contract.
func factory[T proto.Message](p *payloadCodec, desc protoreflect.MessageDescriptor) (func() proto.Message, error) {
	var zero T
	if any(zero) == nil {
		// T is the proto.Message interface: use dynamic messages.
		return func() proto.Message { return dynamicpb.NewMessage(desc) }, nil
	}
	typ := zero.ProtoReflect().Type()
	if err := p.check(typ.Descriptor(), desc); err != nil {
		return nil, err
	}
	return func() proto.Message { return typ.New().Interface() }, nil
}
