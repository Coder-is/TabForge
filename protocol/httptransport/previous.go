package httptransport

import (
	"fmt"
	"strings"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// view is the contract identity a request was accepted under. For the current
// contract endpoint is the current endpoint and legacy is false.
type view struct {
	version, hash string
	endpoint      protocol.ResolvedEndpoint
	types         *protoregistry.Types
	legacy        bool
}

type previousContract struct {
	contract *protocol.Contract
	hash     string
}

// AcceptPrevious additionally serves clients generated from an older contract,
// which makes rolling upgrades possible. Requests carrying the old version and
// schema hash are decoded with the old descriptors, transcoded to the current
// messages for the normal handlers, and every response or event payload is
// transcoded back, validated with the old schema and stamped with the old
// identity, so fields added since then never reach the old client.
//
// Registration is refused unless protocol.TranscodedBreakingChanges reports no
// breaking change from old to the current contract (every old endpoint keeps
// its ID, path, transport, auth and RPC), and the old version differs from the
// current and all previously accepted versions. Like the other options it must
// be called before serving requests. Remove the old contract once its clients
// are gone; Result.ProtocolVersion shows which versions are still in use.
func (s *Server) AcceptPrevious(old *protocol.Contract) error {
	if old == nil {
		return fmt.Errorf("previous contract is required")
	}
	hash := old.Fingerprint()
	s.mu.Lock()
	defer s.mu.Unlock()
	if old.Manifest.Version == s.Contract.Manifest.Version || hash == s.schemaHash {
		return fmt.Errorf("previous contract %s must have a different version and schema than the current contract", old.Manifest.Version)
	}
	for _, p := range s.previous {
		if p.contract.Manifest.Version == old.Manifest.Version || p.hash == hash {
			return fmt.Errorf("previous contract %s is already accepted", old.Manifest.Version)
		}
	}
	if changes := protocol.TranscodedBreakingChanges(old, s.Contract); len(changes) != 0 {
		reasons := make([]string, len(changes))
		for i, c := range changes {
			reasons[i] = c.Path + ": " + c.Reason
		}
		return fmt.Errorf("previous contract %s is incompatible with %s: %s", old.Manifest.Version, s.Contract.Manifest.Version, strings.Join(reasons, "; "))
	}
	s.previous = append(s.previous, previousContract{contract: old, hash: hash})
	return nil
}

// identify resolves the request identity. Versions are unique across the
// current and previous contracts, so the version selects the contract and the
// schema hash must then match it exactly (or be absent if not required).
func (s *Server) identify(e protocol.ResolvedEndpoint, version, hash string) (view, string) {
	match := func(want string) bool {
		return !(s.RequireSchemaHash && hash == "") && (hash == "" || hash == want)
	}
	if version == s.Contract.Manifest.Version {
		if !match(s.schemaHash) {
			return view{}, "schema_mismatch"
		}
		return view{version: version, hash: s.schemaHash, endpoint: e, types: s.Contract.Types}, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.previous {
		if p.contract.Manifest.Version != version {
			continue
		}
		if !match(p.hash) {
			return view{}, "schema_mismatch"
		}
		old, ok := p.contract.Endpoint(e.ID)
		if !ok {
			// A newer endpoint on the same path is not part of this version.
			return view{}, "version_mismatch"
		}
		return view{version: version, hash: p.hash, endpoint: old, types: p.contract.Types, legacy: true}, ""
	}
	return view{}, "version_mismatch"
}

// transcode converts between compatible schemas through the binary wire
// format. Fields unknown to the target, such as response additions, are dropped.
func transcode(message proto.Message, target protoreflect.MessageDescriptor) (proto.Message, error) {
	data, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	out := dynamicpb.NewMessage(target)
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// render validates a handler message against the current contract and returns
// the message and ProtoJSON as seen by the accepted contract.
func (s *Server) render(e protocol.ResolvedEndpoint, v view, message proto.Message) (proto.Message, []byte, error) {
	data, err := s.marshal(e, s.Contract.Types, message)
	if err != nil || !v.legacy {
		return message, data, err
	}
	old, err := transcode(message, v.endpoint.Output)
	if err != nil {
		return nil, nil, err
	}
	data, err = s.marshal(v.endpoint, v.types, old)
	return old, data, err
}
