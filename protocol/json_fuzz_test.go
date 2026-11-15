package protocol

import (
	"fmt"
	"strings"
	"testing"
)

func jsonSeeds() []string {
	seeds := []string{
		`{}`, `[]`, `""`, `0`, `-1.5e+3`, `true`, `null`, ` { } `, `{} []`, `{}x`, `{"a":1}`,
		`{"a":1,"a":2}`, `{"a":1,"a":2}`, `{"é":1,"é":2}`, `{"a\"b":1,"a"b":2}`,
		"{\"\xff\":1,\"\xfe\":2}", "{\"\xff\":1,\"x\":2}", `{"x":{"a":1},"y":{"a":1}}`, `{"x":{"a":1,"a":1}}`,
		`[{"a":1},{"a":2}]`, `{"a":[1,{"b":2,"b":3}]}`, `{"a":1,}`, `[1,]`, `{"a" 1}`, `"\ud800"`, `{"\ud800":1,"\udfff":2}`,
	}
	for _, n := range []int{63, 64, 65, 66} {
		seeds = append(seeds, strings.Repeat("[", n)+strings.Repeat("]", n), strings.Repeat("[", n)+"0"+strings.Repeat("]", n), strings.Repeat(`{"a":`, n)+"1"+strings.Repeat("}", n))
	}
	for _, dup := range []int{-1, 0, 16, 17, 30} {
		var b strings.Builder
		b.WriteString("{")
		for i := 0; i < 32; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			name := fmt.Sprintf("k%d", i)
			if i == 31 && dup >= 0 {
				name = fmt.Sprintf("k%d", dup)
			}
			fmt.Fprintf(&b, "%q:%d", name, i)
		}
		b.WriteString("}")
		seeds = append(seeds, b.String())
	}
	return seeds
}

// The fast path must accept and reject exactly what the token walker does.
func FuzzValidateJSON(f *testing.F) {
	for _, seed := range jsonSeeds() {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fast, slow := ValidateJSON(data), validateJSONSlow(data)
		if (fast == nil) != (slow == nil) {
			t.Fatalf("%q: fast %v, token walker %v", data, fast, slow)
		}
	})
}
