package kcp

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// network delivers datagrams between two KCPs on a virtual clock with
// loss, duplication, reordering (random delay) and an optional outage.
type network struct {
	r                  *rand.Rand
	now                uint32
	loss, dup          float64
	minDelay, maxDelay uint32
	queue              []packet
	down               func(now uint32) bool
	sent, dropped      int
}

type packet struct {
	at   uint32
	to   int
	data []byte
}

func (n *network) send(to int) func([]byte) {
	return func(data []byte) {
		n.sent++
		copies := 1
		if n.r.Float64() < n.dup {
			copies = 2
		}
		for i := 0; i < copies; i++ {
			if n.r.Float64() < n.loss || (n.down != nil && n.down(n.now)) {
				n.dropped++
				continue
			}
			delay := n.minDelay + uint32(n.r.Intn(int(n.maxDelay-n.minDelay)+1))
			n.queue = append(n.queue, packet{at: n.now + delay, to: to, data: append([]byte(nil), data...)})
		}
	}
}

func (n *network) deliver(peers [2]*KCP) {
	sort.SliceStable(n.queue, func(i, j int) bool { return n.queue[i].at < n.queue[j].at })
	i := 0
	for ; i < len(n.queue) && n.queue[i].at <= n.now; i++ {
		if !peers[n.queue[i].to].Input(n.queue[i].data) {
			panic("rejected a datagram")
		}
	}
	n.queue = n.queue[i:]
}

func message(r *rand.Rand, id, maxSize int) []byte {
	size := 8 + r.Intn(maxSize)
	data := make([]byte, size)
	r.Read(data)
	copy(data, fmt.Sprintf("%08d", id))
	return data
}

// transfer sends count messages each way and checks they arrive intact and
// in order. It returns the virtual milliseconds it took.
func transfer(t *testing.T, n *network, configure func(*KCP), count, maxSize int) uint32 {
	t.Helper()
	var peers [2]*KCP
	peers[0] = New(0x11223344, n.send(1))
	peers[1] = New(0x11223344, n.send(0))
	for _, p := range peers {
		configure(p)
	}
	var sent [2][][]byte
	var got [2]int
	next := [2]int{}
	for step := 0; step < 3_000_000; step++ {
		n.now++
		for side, p := range peers {
			// Keep a bounded backlog like a real sender would.
			for next[side] < count && p.WaitSnd() < 256 {
				m := message(n.r, next[side], maxSize)
				if !p.Send(m) {
					t.Fatalf("message of %d bytes refused", len(m))
				}
				sent[side] = append(sent[side], m)
				next[side]++
			}
		}
		n.deliver(peers)
		for side, p := range peers {
			for {
				m := p.Recv()
				if m == nil {
					break
				}
				other := 1 - side
				if got[side] >= len(sent[other]) || !bytes.Equal(m, sent[other][got[side]]) {
					t.Fatalf("side %d message %d corrupted or out of order", side, got[side])
				}
				got[side]++
			}
			p.Update(n.now)
			if p.Dead() {
				t.Fatalf("side %d declared the link dead", side)
			}
		}
		if got[0] == count && got[1] == count {
			return n.now
		}
	}
	t.Fatalf("stalled: received %v of %d", got, count)
	return 0
}

func gameSettings(k *KCP) {
	k.NoDelay(1, 10, 2, true)
	k.WndSize(256, 256)
	k.SetMTU(1200)
	k.SetDeadLink(1 << 30)
}

func TestLossyNetwork(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loss, dup float64
		configure func(*KCP)
	}{
		{"clean-defaults", 0, 0, func(*KCP) {}},
		{"lossy-game", 0.2, 0.05, gameSettings},
		{"lossy-defaults", 0.1, 0.05, func(k *KCP) { k.SetDeadLink(1 << 30) }},
		{"very-lossy-game", 0.4, 0.1, gameSettings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &network{r: rand.New(rand.NewSource(7)), loss: tc.loss, dup: tc.dup, minDelay: 5, maxDelay: 60}
			ms := transfer(t, n, tc.configure, 400, 5000)
			t.Logf("%d virtual ms, %d datagrams, %d dropped", ms, n.sent, n.dropped)
		})
	}
}

func TestLargeMessagesAndOutage(t *testing.T) {
	n := &network{r: rand.New(rand.NewSource(9)), loss: 0.05, minDelay: 10, maxDelay: 30}
	n.down = func(now uint32) bool { return now > 2000 && now < 5000 } // 3 s outage
	transfer(t, n, gameSettings, 60, 64<<10)
}

func TestSendLimits(t *testing.T) {
	k := New(1, func([]byte) {})
	k.SetMTU(1200)
	if !k.Send(make([]byte, MaxFragments*k.MSS())) {
		t.Fatal("largest message refused")
	}
	if k.Send(make([]byte, MaxFragments*k.MSS()+1)) {
		t.Fatal("oversized message accepted")
	}
	if !k.Send(nil) {
		t.Fatal("empty message refused")
	}
}

func TestInputRejectsGarbage(t *testing.T) {
	k := New(5, func([]byte) {})
	other := New(6, func([]byte) {})
	var datagram []byte
	other.output = func(b []byte) { datagram = append([]byte(nil), b...) }
	other.Send([]byte("hi"))
	other.Update(1)
	other.Update(200) // like ikcp, cwnd starts at 0: the first flush sends nothing
	if k.Input(datagram) {
		t.Fatal("accepted another conversation")
	}
	if k.Input(datagram[:10]) || k.Input(nil) {
		t.Fatal("accepted a short datagram")
	}
	bad := append([]byte(nil), datagram...)
	bad[0], bad[1], bad[2], bad[3] = 5, 0, 0, 0
	bad[20] = 200 // length beyond the datagram
	if k.Input(bad) {
		t.Fatal("accepted a truncated segment")
	}
	bad[20], bad[4] = 2, 99 // unknown command
	if k.Input(bad) {
		t.Fatal("accepted an unknown command")
	}
}

func TestDeadLink(t *testing.T) {
	k := New(1, func([]byte) {})
	k.NoDelay(1, 10, 2, true)
	k.SetDeadLink(5)
	k.Send([]byte("x"))
	for now := uint32(0); now < 60000 && !k.Dead(); now += 10 {
		k.Update(now)
	}
	if !k.Dead() {
		t.Fatal("link never declared dead")
	}
}
