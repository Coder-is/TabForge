package realtime

import (
	"runtime"
	"sync"
	"time"
)

// kcpScheduler drives KCP's clock for every conversation of one ServeKCP
// from one goroutine per CPU, instead of a goroutine and a 10 ms ticker per
// conversation. A conversation is visited only while KCP has work (see
// kcp.Idle): Send, Input and Recv put it back with wakeLocked. Idle
// conversations cost nothing; a peer that vanishes while idle is found by
// the session heartbeat, not by KCP.
type kcpScheduler struct {
	shards []kcpShard
	stop   chan struct{}
	once   sync.Once
}

type kcpShard struct {
	mu     sync.Mutex
	active map[*kcpConn]struct{}
}

func newKCPScheduler() *kcpScheduler {
	s := &kcpScheduler{shards: make([]kcpShard, runtime.GOMAXPROCS(0)), stop: make(chan struct{})}
	for i := range s.shards {
		s.shards[i].active = map[*kcpConn]struct{}{}
		go s.run(&s.shards[i])
	}
	return s
}

func (s *kcpScheduler) close() { s.once.Do(func() { close(s.stop) }) }

func (s *kcpScheduler) shard(c *kcpConn) *kcpShard {
	return &s.shards[int(c.kcp.Conv()%uint32(len(s.shards)))]
}

// add and remove are called with c.mu held (lock order: c.mu, then shard.mu).
func (s *kcpScheduler) add(c *kcpConn) {
	sh := s.shard(c)
	sh.mu.Lock()
	sh.active[c] = struct{}{}
	sh.mu.Unlock()
}

func (s *kcpScheduler) remove(c *kcpConn) {
	sh := s.shard(c)
	sh.mu.Lock()
	delete(sh.active, c)
	sh.mu.Unlock()
}

func (s *kcpScheduler) run(sh *kcpShard) {
	t := time.NewTicker(kcpInterval)
	defer t.Stop()
	var due []*kcpConn
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		sh.mu.Lock()
		due = due[:0]
		for c := range sh.active {
			due = append(due, c)
		}
		sh.mu.Unlock()
		for i, c := range due {
			c.step()
			due[i] = nil
		}
	}
}
