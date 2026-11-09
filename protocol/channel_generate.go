package protocol

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

type channelMeta struct {
	Path           string                 `json:"path"`
	Auth           string                 `json:"auth"`
	HeartbeatMS    int                    `json:"heartbeatMS"`
	ResumeWindowMS int                    `json:"resumeWindowMS"`
	Messages       map[string]messageMeta `json:"messages"`
}

type messageMeta struct {
	ID           uint32 `json:"id"`
	Kind         string `json:"kind"`
	Type         string `json:"type"`
	ResponseType string `json:"responseType,omitempty"`
	TimeoutMS    int    `json:"timeoutMS,omitempty"`
	Volatile     bool   `json:"volatile,omitempty"`
}

func (c *Contract) channelMetadata() map[string]channelMeta {
	out := map[string]channelMeta{}
	for _, ch := range c.Channels {
		meta := channelMeta{Path: ch.Path, Auth: ch.Auth, HeartbeatMS: ch.Heartbeat, ResumeWindowMS: ch.Resume, Messages: map[string]messageMeta{}}
		for _, m := range ch.Messages {
			mm := messageMeta{ID: m.ID, Kind: m.Kind, Type: string(m.Input.FullName()), TimeoutMS: m.TimeoutMS, Volatile: m.Volatile}
			if m.Output != nil {
				mm.ResponseType = string(m.Output.FullName())
			}
			meta.Messages[m.Name] = mm
		}
		out[ch.ID] = meta
	}
	return out
}

// CodecSchema is the field-number view realtime clients need to translate
// between binary Protobuf and ProtoJSON-shaped objects. WireSchema is keyed by
// JSON name and has no field numbers; it stays unchanged for HTTP clients.
type CodecSchema struct {
	Messages map[string][]CodecField `json:"messages"`
	// Enum values in declaration order; aliases decode to the first name.
	Enums map[string][]CodecEnumValue `json:"enums"`
}

type CodecField struct {
	Number int32  `json:"no"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Type   string `json:"type,omitempty"`
	List   bool   `json:"list,omitempty"`
	Packed bool   `json:"packed,omitempty"`
	MapKey string `json:"mapKey,omitempty"`
	// Presence marks explicit presence: a set default value is still encoded.
	Presence bool `json:"presence,omitempty"`
	// Oneof is the 1-based index of a real oneof the field belongs to.
	Oneof int `json:"oneof,omitempty"`
}

type CodecEnumValue struct {
	Name   string
	Number int32
}

func (v CodecEnumValue) MarshalJSON() ([]byte, error) {
	return json.Marshal([]interface{}{v.Name, v.Number})
}

func (c *Contract) CodecSchema() CodecSchema {
	s := CodecSchema{Messages: map[string][]CodecField{}, Enums: map[string][]CodecEnumValue{}}
	var visit func(protoreflect.MessageDescriptor)
	visitEnum := func(e protoreflect.EnumDescriptor) {
		if _, ok := s.Enums[string(e.FullName())]; ok {
			return
		}
		values := []CodecEnumValue{}
		for i := 0; i < e.Values().Len(); i++ {
			v := e.Values().Get(i)
			values = append(values, CodecEnumValue{string(v.Name()), int32(v.Number())})
		}
		s.Enums[string(e.FullName())] = values
	}
	visit = func(m protoreflect.MessageDescriptor) {
		if _, ok := s.Messages[string(m.FullName())]; ok {
			return
		}
		fields := []CodecField{}
		s.Messages[string(m.FullName())] = fields
		oneofs := map[protoreflect.FullName]int{}
		for i, n := 0, 0; i < m.Oneofs().Len(); i++ {
			if o := m.Oneofs().Get(i); !o.IsSynthetic() {
				n++
				oneofs[o.FullName()] = n
			}
		}
		for i := 0; i < m.Fields().Len(); i++ {
			f := m.Fields().Get(i)
			value := f
			field := CodecField{Number: int32(f.Number()), Name: f.JSONName(), List: f.IsList(), Packed: f.IsPacked(), Presence: f.HasPresence()}
			if f.IsMap() {
				value = f.MapValue()
				field.MapKey = f.MapKey().Kind().String()
				field.List, field.Packed, field.Presence = false, false, false
			}
			if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
				field.Oneof = oneofs[o.FullName()]
			}
			field.Kind = value.Kind().String()
			if value.Message() != nil {
				field.Type = string(value.Message().FullName())
				visit(value.Message())
			}
			if value.Enum() != nil {
				field.Type = string(value.Enum().FullName())
				visitEnum(value.Enum())
			}
			fields = append(fields, field)
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Number < fields[j].Number })
		s.Messages[string(m.FullName())] = fields
	}
	for _, ch := range c.Channels {
		for _, m := range ch.Messages {
			visit(m.Input)
			if m.Output != nil {
				visit(m.Output)
			}
		}
	}
	return s
}

func (c *Contract) channelTypeScript(b *strings.Builder) error {
	if len(c.Channels) == 0 {
		return nil
	}
	b.WriteString("\nexport interface ChannelTypes {\n")
	for _, ch := range c.Channels {
		fmt.Fprintf(b, "  %s: {\n", quote(ch.ID))
		for _, section := range []struct{ name, kind string }{{"requests", KindRequest}, {"pushes", KindPush}, {"notifies", KindNotify}} {
			fmt.Fprintf(b, "    %s: {", section.name)
			any := false
			for _, m := range ch.Messages {
				if m.Kind != section.kind {
					continue
				}
				any = true
				if m.Kind == KindRequest {
					fmt.Fprintf(b, "\n      %s: { request: %s; response: %s };", quote(m.Name), tsName(m.Input.FullName()), tsName(m.Output.FullName()))
				} else {
					fmt.Fprintf(b, "\n      %s: %s;", quote(m.Name), tsName(m.Input.FullName()))
				}
			}
			if any {
				b.WriteString("\n    ")
			}
			b.WriteString("};\n")
		}
		b.WriteString("  };\n")
	}
	b.WriteString("}\n\nexport const channels = ")
	data, err := json.MarshalIndent(c.channelMetadata(), "", "  ")
	if err != nil {
		return err
	}
	b.WriteString(strings.ReplaceAll(string(data), `"__proto__":`, `["__proto__"]:`))
	b.WriteString(" as const;\nexport const codecSchema = ")
	data, err = json.Marshal(c.CodecSchema())
	if err != nil {
		return err
	}
	b.WriteString(strings.ReplaceAll(string(data), `"__proto__":`, `["__proto__"]:`))
	b.WriteString(" as const;\n")
	return nil
}

func (c *Contract) channelMarkdown() string {
	if len(c.Channels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Realtime channels\n\nChannels are WebSocket connections. The client offers subprotocol `tabforge.realtime.v1.proto` (binary frames, Protobuf payloads) or `tabforge.realtime.v1.json` (text frames, ProtoJSON payloads). The first frame is `hello` (protocol version, schema hash, bearer token, or session id + resume token); the server answers `welcome` or `close`. Request, response, push and notify frames are numbered per direction and acknowledged; after a reconnect the session resumes and unacknowledged frames are retransmitted, so each is delivered exactly once and in order while the session lives. Volatile messages are not numbered or retransmitted. The client sends `ping` every heartbeat interval. Frame layout and recovery rules: [realtime protocol](https://github.com/Coder-is/TabForge/blob/main/doc/realtime.md).\n\n")
	for _, ch := range c.Channels {
		resume := "disabled"
		if ch.Resume > 0 {
			resume = fmt.Sprintf("%d ms", ch.Resume)
		}
		fmt.Fprintf(&b, "### Channel %s\n\n- Path: `%s` (WebSocket)\n- Auth: `%s`\n- Heartbeat: %d ms\n- Resume window: %s\n\n| ID | Name | Kind | Payload | Response | Timeout | Delivery |\n| --- | --- | --- | --- | --- | --- | --- |\n", md(ch.ID), ch.Path, ch.Auth, ch.Heartbeat, resume)
		for _, m := range ch.Messages {
			response, timeout, delivery := "", "", "reliable"
			if m.Output != nil {
				response = string(m.Output.FullName())
				timeout = fmt.Sprintf("%d ms", m.TimeoutMS)
			}
			if m.Volatile {
				delivery = "volatile"
			}
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s |\n", m.ID, md(m.Name), m.Kind, m.Input.FullName(), response, timeout, delivery)
		}
		b.WriteString("\n")
	}
	return b.String()
}
