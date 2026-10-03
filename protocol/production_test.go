package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestStrictJSONDuplicatesAndDepth(t *testing.T) {
	for _, input := range []string{`{"id":1,"id":2}`, `{"nested":{"x":1,"x":2}}`, `{} []`, strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70)} {
		if ValidateJSON([]byte(input)) == nil {
			t.Fatalf("ambiguous input accepted: %s", input)
		}
	}
	if err := ValidateJSON([]byte(`{"x":[1,{"id":"18446744073709551615"}],"value":null}`)); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintAndCompatibility(t *testing.T) {
	old, set := example(t)
	copy := proto.Clone(set).(*descriptorpb.FileDescriptorSet)
	for _, file := range copy.File {
		file.SourceCodeInfo = nil
	}
	same, err := Resolve(old.Manifest, copy)
	if err != nil {
		t.Fatal(err)
	}
	if old.Fingerprint() != same.Fingerprint() {
		t.Fatal("comments affect fingerprint")
	}
	if changes := BreakingChanges(old, same); len(changes) != 0 {
		t.Fatalf("unchanged contract broke: %v", changes)
	}
	// An optional request field preserves old clients, but changes exact identity.
	copy.File[0].MessageType[0].Field = append(copy.File[0].MessageType[0].Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("client_hint"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()})
	requestChange, err := Resolve(old.Manifest, copy)
	if err != nil {
		t.Fatal(err)
	}
	if len(BreakingChanges(old, requestChange)) != 0 || old.Fingerprint() == requestChange.Fingerprint() {
		t.Fatal("optional request change misclassified")
	}
	copy.File[0].MessageType[1].Field = append(copy.File[0].MessageType[1].Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("extra"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()})
	responseChange, err := Resolve(old.Manifest, copy)
	if err != nil {
		t.Fatal(err)
	}
	if len(BreakingChanges(old, responseChange)) == 0 {
		t.Fatal("strict client response addition incorrectly accepted")
	}
	copy.File[0].MessageType[0].Field[0].JsonName = proto.String("renamed")
	renamed, err := Resolve(old.Manifest, copy)
	if err != nil {
		t.Fatal(err)
	}
	if len(BreakingChanges(old, renamed)) < 2 {
		t.Fatal("JSON name change not detected")
	}
}
