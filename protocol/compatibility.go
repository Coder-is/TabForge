package protocol

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
)

type BreakingChange struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// BreakingChanges checks whether newContract preserves an old client's contract.
// Response field/event additions are breaking for the strict JSON SDK profile.
// This is a rollout check, not version negotiation or a binary-only comparison.
func BreakingChanges(oldContract, newContract *Contract) []BreakingChange {
	var changes []BreakingChange
	add := func(path, reason string) { changes = append(changes, BreakingChange{path, reason}) }
	seen := map[string]bool{}
	var compare func(protoreflect.MessageDescriptor, protoreflect.MessageDescriptor, bool)
	compare = func(old, next protoreflect.MessageDescriptor, response bool) {
		key := fmt.Sprintf("%s/%t", old.FullName(), response)
		if seen[key] {
			return
		}
		seen[key] = true
		if old.FullName() != next.FullName() {
			add(string(old.FullName()), "message type changed")
			return
		}
		for i := 0; i < old.Fields().Len(); i++ {
			field := old.Fields().Get(i)
			target := next.Fields().ByNumber(field.Number())
			path := string(old.FullName()) + "." + string(field.Name())
			if target == nil {
				add(path, "field removed")
				continue
			}
			if field.JSONName() != target.JSONName() || field.Kind() != target.Kind() || field.Cardinality() != target.Cardinality() || field.IsMap() != target.IsMap() || field.HasPresence() != target.HasPresence() || oneofName(field) != oneofName(target) {
				add(path, "wire type, JSON name or presence changed")
				continue
			}
			if field.HasDefault() != target.HasDefault() || (field.HasDefault() && fmt.Sprint(field.Default().Interface()) != fmt.Sprint(target.Default().Interface())) {
				add(path, "explicit default changed")
			}
			if field.IsMap() {
				if field.MapKey().Kind() != target.MapKey().Kind() {
					add(path, "map key type changed")
				}
				field, target = field.MapValue(), target.MapValue()
				if field.Kind() != target.Kind() {
					add(path, "map value type changed")
					continue
				}
			}
			if field.Message() != nil {
				compare(field.Message(), target.Message(), response)
			}
			if field.Enum() != nil {
				if field.Enum().FullName() != target.Enum().FullName() {
					add(path, "enum type changed")
					continue
				}
				for j := 0; j < field.Enum().Values().Len(); j++ {
					value := field.Enum().Values().Get(j)
					replacement := target.Enum().Values().ByName(value.Name())
					if replacement == nil || replacement.Number() != value.Number() {
						add(path, "enum member removed or renumbered")
					}
				}
				if response {
					for j := 0; j < target.Enum().Values().Len(); j++ {
						if field.Enum().Values().ByName(target.Enum().Values().Get(j).Name()) == nil {
							add(path, "new response enum name is unknown to old clients")
						}
					}
				}
			}
		}
		for i := 0; i < next.Fields().Len(); i++ {
			f := next.Fields().Get(i)
			if old.Fields().ByNumber(f.Number()) == nil && (response || f.Cardinality() == protoreflect.Required) {
				add(string(next.FullName())+"."+string(f.Name()), "new response or required field")
			}
		}
	}
	for _, old := range oldContract.Endpoints {
		next, found := newContract.Endpoint(old.ID)
		if !found {
			add(old.ID, "endpoint removed")
			continue
		}
		if old.Path != next.Path || old.Transport != next.Transport || old.Auth != next.Auth || old.RPC != next.RPC {
			add(old.ID, "route, transport, auth or RPC changed")
		}
		if next.TimeoutMS < old.TimeoutMS {
			add(old.ID, "deadline shortened")
		}
		compare(old.Input, next.Input, false)
		compare(old.Output, next.Output, true)
		oldEvents := map[string]Event{}
		nextEvents := map[string]Event{}
		for _, e := range old.Events {
			oldEvents[e.Name] = e
		}
		for _, e := range next.Events {
			nextEvents[e.Name] = e
		}
		for name, event := range oldEvents {
			if replacement, ok := nextEvents[name]; !ok || replacement != event {
				add(old.ID+"."+name, "event removed or framing changed")
			}
		}
		for name := range nextEvents {
			if _, ok := oldEvents[name]; !ok {
				add(old.ID+"."+name, "new event is unknown to old clients")
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Reason < changes[j].Reason
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func oneofName(f protoreflect.FieldDescriptor) string {
	if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
		return string(o.Name())
	}
	return ""
}
