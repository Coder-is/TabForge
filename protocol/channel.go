package protocol

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Channel is a long-lived bidirectional connection (WebSocket in v1). One
// connection carries three message kinds: request (client asks, server answers
// once), push (server to client) and notify (client to server, no answer).
type Channel struct {
	ID        string           `json:"id"`
	Path      string           `json:"path"`
	Auth      string           `json:"auth"` // none or bearer; the token travels in the hello frame.
	Heartbeat int              `json:"heartbeat_ms"`
	Resume    int              `json:"resume_window_ms"` // 0 disables session resume.
	Messages  []ChannelMessage `json:"messages"`
}

// ChannelMessage is identified on the wire by ID; Name is the source-level
// handle used by generated code. Requests reference a unary RPC, push and
// notify reference a message type.
type ChannelMessage struct {
	ID        uint32 `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	RPC       string `json:"rpc,omitempty"`
	Type      string `json:"type,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	// Volatile push/notify frames are latest-state updates (positions, HP bars):
	// they are not sequenced, buffered or retransmitted, and are dropped while
	// disconnected or when the send queue is full.
	Volatile bool `json:"volatile,omitempty"`
}

const (
	KindRequest = "request"
	KindPush    = "push"
	KindNotify  = "notify"
	// MaxMessageID keeps identifiers compact and representable everywhere.
	MaxMessageID = 65535
)

type ResolvedMessage struct {
	ChannelMessage
	// Input is the request, push or notify payload; Output is set for requests.
	Input  protoreflect.MessageDescriptor
	Output protoreflect.MessageDescriptor
}

type ResolvedChannel struct {
	Channel
	Messages []ResolvedMessage
}

func (c ResolvedChannel) Message(name string) (ResolvedMessage, bool) {
	for _, m := range c.Messages {
		if m.Name == name {
			return m, true
		}
	}
	return ResolvedMessage{}, false
}

func (c ResolvedChannel) MessageByID(id uint32) (ResolvedMessage, bool) {
	for _, m := range c.Messages {
		if m.ID == id {
			return m, true
		}
	}
	return ResolvedMessage{}, false
}

func (c *Contract) Channel(id string) (ResolvedChannel, bool) {
	for _, ch := range c.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return ResolvedChannel{}, false
}

func (c *Contract) resolveChannels(paths map[string]bool) error {
	ids := map[string]bool{}
	for _, ch := range c.Manifest.Channels {
		if !identifier.MatchString(ch.ID) || ids[ch.ID] {
			return fmt.Errorf("invalid or duplicate channel id %q", ch.ID)
		}
		ids[ch.ID] = true
		if !route.MatchString(ch.Path) || strings.Contains(ch.Path, "//") || strings.HasSuffix(ch.Path, "/") || paths[ch.Path] {
			return fmt.Errorf("%s: invalid or duplicate channel path %q", ch.ID, ch.Path)
		}
		for _, segment := range strings.Split(ch.Path, "/") {
			if segment == "." || segment == ".." {
				return fmt.Errorf("%s: invalid path segment in %q", ch.ID, ch.Path)
			}
		}
		paths[ch.Path] = true
		if ch.Auth != "none" && ch.Auth != "bearer" {
			return fmt.Errorf("%s: auth must be none or bearer", ch.ID)
		}
		if ch.Heartbeat < 1000 || ch.Heartbeat > 600000 {
			return fmt.Errorf("%s: heartbeat_ms must be between 1000 and 600000", ch.ID)
		}
		if ch.Resume < 0 || ch.Resume > 3600000 {
			return fmt.Errorf("%s: resume_window_ms must be between 0 and 3600000", ch.ID)
		}
		if len(ch.Messages) == 0 {
			return fmt.Errorf("%s: a channel needs at least one message", ch.ID)
		}
		resolved := ResolvedChannel{Channel: ch}
		names, numbers := map[string]bool{}, map[uint32]bool{}
		for _, m := range ch.Messages {
			where := ch.ID + "." + m.Name
			if !identifier.MatchString(m.Name) || names[m.Name] {
				return fmt.Errorf("%s: invalid or duplicate message name %q", ch.ID, m.Name)
			}
			if m.ID == 0 || m.ID > MaxMessageID || numbers[m.ID] {
				return fmt.Errorf("%s: message id must be unique and between 1 and %d", where, MaxMessageID)
			}
			names[m.Name], numbers[m.ID] = true, true
			r := ResolvedMessage{ChannelMessage: m}
			switch m.Kind {
			case KindRequest:
				if m.Type != "" || m.Volatile || m.TimeoutMS <= 0 || m.TimeoutMS > 86400000 {
					return fmt.Errorf("%s: request needs rpc and timeout_ms between 1 and 86400000, and no type or volatile", where)
				}
				desc, err := c.Files.FindDescriptorByName(protoreflect.FullName(m.RPC))
				if err != nil {
					return fmt.Errorf("%s: RPC %q: %v", where, m.RPC, err)
				}
				rpc, ok := desc.(protoreflect.MethodDescriptor)
				if !ok || rpc.IsStreamingClient() || rpc.IsStreamingServer() {
					return fmt.Errorf("%s: request RPC must be unary (use push messages for server streams)", where)
				}
				r.Input, r.Output = rpc.Input(), rpc.Output()
			case KindPush, KindNotify:
				if m.RPC != "" || m.TimeoutMS != 0 {
					return fmt.Errorf("%s: %s takes type, not rpc or timeout_ms", where, m.Kind)
				}
				desc, err := c.Files.FindDescriptorByName(protoreflect.FullName(m.Type))
				if err != nil {
					return fmt.Errorf("%s: type %q: %v", where, m.Type, err)
				}
				message, ok := desc.(protoreflect.MessageDescriptor)
				if !ok || message.IsMapEntry() {
					return fmt.Errorf("%s: type %q is not a message", where, m.Type)
				}
				r.Input = message
			default:
				return fmt.Errorf("%s: kind must be request, push or notify", where)
			}
			for _, desc := range []protoreflect.MessageDescriptor{r.Input, r.Output} {
				if desc != nil {
					if err := realtimeCodecSupports(desc, map[protoreflect.FullName]bool{}); err != nil {
						return fmt.Errorf("%s: %v", where, err)
					}
				}
			}
			resolved.Messages = append(resolved.Messages, r)
		}
		c.Channels = append(c.Channels, resolved)
	}
	return nil
}

// realtimeCodecSupports limits channel messages to types every realtime codec
// can translate between binary Protobuf and ProtoJSON without a type registry.
func realtimeCodecSupports(m protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) error {
	if seen[m.FullName()] {
		return nil
	}
	seen[m.FullName()] = true
	switch m.FullName() {
	case "google.protobuf.Any", "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.ListValue", "google.protobuf.FieldMask":
		return fmt.Errorf("%s is not supported in channel messages; use an explicit message or bytes", m.FullName())
	}
	for i := 0; i < m.Fields().Len(); i++ {
		f := m.Fields().Get(i)
		if f.Kind() == protoreflect.GroupKind {
			return fmt.Errorf("%s.%s: groups are not supported in channel messages", m.FullName(), f.Name())
		}
		if f.IsMap() {
			f = f.MapValue()
		}
		if f.Message() != nil {
			if err := realtimeCodecSupports(f.Message(), seen); err != nil {
				return err
			}
		}
	}
	return nil
}
