package bindata

import (
	"bytes"
	"testing"

	"github.com/Coder-is/TabForge/v3/model"
)

func TestUnsignedBinaryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		kind, maximum, overflow string
		width                   int
	}{
		{"uint16", "65535", "65536", 2},
		{"uint32", "4294967295", "4294967296", 4},
		{"uint64", "18446744073709551615", "18446744073709551616", 8},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			field := &model.TypeDefine{FieldType: tc.kind}
			for _, value := range []string{"", "0", tc.maximum, "-1", tc.overflow} {
				writer := NewBinaryWriter()
				err := writeValue(model.NewGlobals(), writer, field, tc.kind, value)
				if value == "-1" || value == tc.overflow {
					if err == nil || len(writer.Bytes()) != 0 {
						t.Fatalf("invalid value %s was written: %x, %v", value, writer.Bytes(), err)
					}
					continue
				}
				var fill byte
				if value == tc.maximum {
					fill = 0xff
				}
				if err != nil || !bytes.Equal(writer.Bytes(), bytes.Repeat([]byte{fill}, tc.width)) {
					t.Fatalf("value %s: %x, %v", value, writer.Bytes(), err)
				}
			}
		})
	}
}
