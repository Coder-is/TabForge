package realtime

import (
	"fmt"
	"sync"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
)

// Group is a set of sessions (a room, a guild, a match) that receive the same
// pushes. The payload is encoded once per encoding. Sessions leave the group
// automatically when they end.
type Group struct {
	mu       sync.RWMutex
	sessions map[*Session]struct{}
}

func NewGroup() *Group { return &Group{sessions: map[*Session]struct{}{}} }

func (g *Group) Add(s *Session) {
	g.mu.Lock()
	_, exists := g.sessions[s]
	g.sessions[s] = struct{}{}
	g.mu.Unlock()
	if !exists {
		s.OnEnd(func(*Error) { g.Remove(s) })
	}
}

func (g *Group) Remove(s *Session) {
	g.mu.Lock()
	delete(g.sessions, s)
	g.mu.Unlock()
}

func (g *Group) Len() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.sessions)
}

// Members returns a snapshot of the group.
func (g *Group) Members() []*Session {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Session, 0, len(g.sessions))
	for s := range g.sessions {
		out = append(out, s)
	}
	return out
}

// Push sends a push to every member except those for which skip returns
// true (for example the sender). Per-session failures (a slow client closed
// with overloaded, a dropped volatile frame) do not stop the broadcast; the
// number of sessions that accepted the frame is returned.
func (g *Group) Push(name string, message proto.Message, skip func(*Session) bool) (int, error) {
	members := g.Members()
	if len(members) == 0 {
		return 0, nil
	}
	var encoded [2][]byte
	var m protocol.ResolvedMessage
	sent := 0
	for _, s := range members {
		if skip != nil && skip(s) {
			continue
		}
		if m.Name == "" {
			var ok bool
			if m, ok = s.channel.Message(name); !ok || m.Kind != protocol.KindPush {
				return 0, fmt.Errorf("%s: no push message %q", s.channel.ID, name)
			}
		}
		if encoded[s.encoding] == nil {
			payload, err := s.srv.codec.encode(s.encoding, m.Input, message)
			if err != nil {
				return sent, err
			}
			encoded[s.encoding] = payload
		}
		if s.pushEncoded(m, encoded[s.encoding]) == nil {
			sent++
		}
	}
	return sent, nil
}
