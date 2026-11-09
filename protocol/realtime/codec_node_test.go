package realtime

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	return node
}

// randomMessage fills every kind of field the example schema uses, including
// extreme integers, special floats, unicode, maps, oneofs and timestamps.
func randomMessage(r *rand.Rand, desc protoreflect.MessageDescriptor, depth int) *dynamicpb.Message {
	m := dynamicpb.NewMessage(desc)
	switch desc.FullName() {
	case "google.protobuf.Timestamp":
		m.Set(desc.Fields().ByName("seconds"), protoreflect.ValueOfInt64(r.Int63n(253402300799+62135596800)-62135596800))
		m.Set(desc.Fields().ByName("nanos"), protoreflect.ValueOfInt32(pick(r, 0, int32(r.Intn(1e9)), 5000000, 123000)))
		return m
	}
	oneofDone := map[protoreflect.FullName]bool{}
	for i := 0; i < desc.Fields().Len(); i++ {
		f := desc.Fields().Get(i)
		if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
			if oneofDone[o.FullName()] || r.Intn(2) == 0 {
				continue
			}
			oneofDone[o.FullName()] = true
		} else if r.Intn(4) == 0 {
			continue
		}
		switch {
		case f.IsMap():
			mp := m.Mutable(f).Map()
			for n := r.Intn(4); n > 0; n-- {
				mp.Set(randomScalar(r, f.MapKey(), depth).MapKey(), randomScalar(r, f.MapValue(), depth))
			}
		case f.IsList():
			list := m.Mutable(f).List()
			for n := r.Intn(4); n > 0; n-- {
				list.Append(randomScalar(r, f, depth))
			}
		case f.Message() != nil:
			if depth < 4 {
				m.Set(f, protoreflect.ValueOfMessage(randomMessage(r, f.Message(), depth+1)))
			}
		default:
			m.Set(f, randomScalar(r, f, depth))
		}
	}
	return m
}

func pick[T any](r *rand.Rand, values ...T) T { return values[r.Intn(len(values))] }

func randomScalar(r *rand.Rand, f protoreflect.FieldDescriptor, depth int) protoreflect.Value {
	switch f.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(r.Intn(2) == 0)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(pick(r, "", "a", "勇者😀", "line\nbreak\"quote\\", string(rune(0x10FFFF))))
	case protoreflect.BytesKind:
		b := make([]byte, r.Intn(6))
		r.Read(b)
		return protoreflect.ValueOfBytes(b)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(pick(r, 0, 1, -1, math.MaxInt32, math.MinInt32, r.Int31()-r.Int31()))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(pick(r, 0, 1, math.MaxUint32, r.Uint32()))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(pick(r, 0, 1, -1, math.MaxInt64, math.MinInt64, r.Int63()-r.Int63(), 1<<53+1))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(pick(r, 0, 1, math.MaxUint64, r.Uint64(), 1<<53+1))
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(pick(r, 0, 1.5, -2.25, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), math.MaxFloat32, math.SmallestNonzeroFloat32, r.Float32()*1e6))
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(pick(r, 0, 0.1, math.NaN(), math.Inf(-1), math.MaxFloat64, r.NormFloat64()))
	case protoreflect.EnumKind:
		values := f.Enum().Values()
		if r.Intn(10) == 0 {
			return protoreflect.ValueOfEnum(77) // Unknown to the schema: stays a number.
		}
		return protoreflect.ValueOfEnum(values.Get(r.Intn(values.Len())).Number())
	case protoreflect.MessageKind:
		return protoreflect.ValueOfMessage(randomMessage(r, f.Message(), depth+1))
	}
	panic(f.Kind().String())
}

type codecCase struct {
	Type   string `json:"type"`
	Binary string `json:"binary"`
	JSON   string `json:"json"`
}

func TestTypeScriptCodecMatchesGo(t *testing.T) {
	node := nodeOrSkip(t)
	c := testContract(t, nil)
	var types []protoreflect.MessageDescriptor
	for name := range c.CodecSchema().Messages {
		desc, err := c.Files.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, desc.(protoreflect.MessageDescriptor))
	}
	r := rand.New(rand.NewSource(42))
	var cases []codecCase
	var originals []proto.Message
	for i := 0; i < 3000; i++ {
		desc := types[i%len(types)]
		if desc.FullName() == "google.protobuf.Timestamp" {
			continue
		}
		m := randomMessage(r, desc, 0)
		binary, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		text, err := protojson.MarshalOptions{Resolver: c.Types}.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, codecCase{Type: string(desc.FullName()), Binary: base64.StdEncoding.EncodeToString(binary), JSON: string(text)})
		originals = append(originals, m)
	}
	dir := t.TempDir()
	input, output := filepath.Join(dir, "cases.json"), filepath.Join(dir, "results.json")
	data, _ := json.Marshal(cases)
	if err := os.WriteFile(input, data, 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "../../sdk/typescript/codec.integration.mjs", input, output).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	t.Log(string(out))
	var results []struct{ FromJSON, FromDecoded string }
	data, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &results); err != nil || len(results) != len(cases) {
		t.Fatalf("results: %v %d", err, len(results))
	}
	for i, result := range results {
		for _, encoded := range []string{result.FromJSON, result.FromDecoded} {
			binary, _ := base64.StdEncoding.DecodeString(encoded)
			m := dynamicpb.NewMessage(originals[i].ProtoReflect().Descriptor())
			if err := proto.Unmarshal(binary, m); err != nil {
				t.Fatalf("case %d: Go cannot decode TypeScript output: %v", i, err)
			}
			if !proto.Equal(m, originals[i]) {
				t.Fatalf("case %d %s: TypeScript re-encoding differs\nwant %v\ngot  %v", i, cases[i].Type, originals[i], m)
			}
		}
	}
}

var _ = protocol.KindPush
