// A small realtime game server for the TabForge channel example: login,
// rooms with movement broadcast, chat and purchases that run exactly once
// even when the network drops mid-request.
//
//	go run ./examples/realtime/server
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/protocol/realtime"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type player struct {
	id       uint64
	nickname string
	mu       sync.Mutex
	gold     int64
	items    map[uint32]uint32
	room     *realtime.Group
}

func main() {
	contractPath := flag.String("contract", "examples/realtime/Generated/protocol/contract.json", "generated realtime contract")
	address := flag.String("listen", "127.0.0.1:18090", "listen address")
	kcpAddress := flag.String("kcp", "", "also serve KCP over UDP on this address, e.g. 127.0.0.1:18091")
	kcpKey := flag.String("kcp-key", "", "base64 X25519 private key for encrypted KCP (with -kcp); \"generate\" makes one, \"\" keeps KCP in clear text")
	flag.Parse()
	c, err := protocol.Load(*contractPath)
	if err != nil {
		log.Fatal(err)
	}
	server := realtime.NewServer(c)
	// Demo tokens are "user-<id>". Replace with real token verification.
	server.Authenticate = func(ctx context.Context, channel, token string) (realtime.Identity, error) {
		id, err := strconv.ParseUint(strings.TrimPrefix(token, "user-"), 10, 64)
		if !strings.HasPrefix(token, "user-") || err != nil {
			return realtime.Identity{}, &realtime.Error{Code: realtime.CodeUnauthorized, Message: "invalid token"}
		}
		return realtime.Identity{UserID: strconv.FormatUint(id, 10)}, nil
	}
	server.SingleSessionPerUser = true
	server.Observe = func(e realtime.Event) {
		if e.Type != "request" && e.Type != "notify" || e.Code != "" {
			log.Printf("%s channel=%s session=%s user=%s message=%s code=%s %v", e.Type, e.Channel, e.SessionID, e.UserID, e.Message, e.Code, e.Duration)
		}
	}
	var rooms sync.Map
	var chatID atomic.Uint64
	playerOf := func(s *realtime.Session) (*player, error) {
		p, ok := s.Values.Load("player")
		if !ok {
			return nil, &realtime.Error{Code: "not_logged_in", Message: "call login first"}
		}
		return p.(*player), nil
	}
	must := func(err error) {
		if err != nil {
			log.Fatal(err)
		}
	}
	must(realtime.OnRequest(server, "game", "login", func(ctx context.Context, s *realtime.Session, req *gamepb.LoginRequest) (*gamepb.LoginReply, error) {
		id, _ := strconv.ParseUint(s.Identity().UserID, 10, 64)
		p := &player{id: id, nickname: req.GetNickname(), gold: 1000, items: map[uint32]uint32{}}
		s.Values.Store("player", p)
		return &gamepb.LoginReply{PlayerId: id, Nickname: p.nickname, Gold: p.gold}, nil
	}))
	must(realtime.OnRequest(server, "game", "joinRoom", func(ctx context.Context, s *realtime.Session, req *gamepb.JoinRoomRequest) (*gamepb.RoomState, error) {
		p, err := playerOf(s)
		if err != nil {
			return nil, err
		}
		group, _ := rooms.LoadOrStore(req.GetRoom(), realtime.NewGroup())
		p.room = group.(*realtime.Group)
		p.room.Add(s)
		state := &gamepb.RoomState{Room: req.GetRoom()}
		for _, member := range p.room.Members() {
			if other, err := playerOf(member); err == nil {
				state.Players = append(state.Players, &gamepb.Player{PlayerId: other.id, Nickname: other.nickname})
			}
		}
		return state, nil
	}))
	must(realtime.OnNotify(server, "game", "move", func(ctx context.Context, s *realtime.Session, m *gamepb.Move) {
		if p, err := playerOf(s); err == nil && p.room != nil {
			p.room.Push("playerMoved", &gamepb.PlayerMoved{PlayerId: p.id, Position: m.GetPosition(), Tick: m.GetTick()}, func(other *realtime.Session) bool { return other == s })
		}
	}))
	must(realtime.OnRequest(server, "game", "chat", func(ctx context.Context, s *realtime.Session, req *gamepb.ChatSend) (*gamepb.ChatAck, error) {
		p, err := playerOf(s)
		if err != nil {
			return nil, err
		}
		if p.room == nil {
			return nil, &realtime.Error{Code: "not_in_room", Message: "join a room first"}
		}
		id := chatID.Add(1)
		p.room.Push("chatMessage", &gamepb.ChatMessage{MessageId: id, From: p.id, Text: req.GetText(), SentAt: timestamppb.Now()}, nil)
		return &gamepb.ChatAck{MessageId: id}, nil
	}))
	// Requests of one session run in order and at most once, so a purchase
	// retransmitted after a reconnect is never charged twice.
	must(realtime.OnRequest(server, "game", "buy", func(ctx context.Context, s *realtime.Session, req *gamepb.BuyRequest) (*gamepb.BuyReply, error) {
		p, err := playerOf(s)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		cost := int64(req.GetCount()) * 10
		if req.GetCount() == 0 || cost > p.gold {
			return nil, &realtime.Error{Code: "not_enough_gold", Message: "not enough gold"}
		}
		p.gold -= cost
		p.items[req.GetItemId()] += req.GetCount()
		inventory := map[uint32]uint32{}
		for k, v := range p.items {
			inventory[k] = v
		}
		return &gamepb.BuyReply{Gold: p.gold, Inventory: inventory}, nil
	}))

	var udp net.PacketConn
	if *kcpAddress != "" {
		if *kcpKey != "" {
			raw, err := base64.StdEncoding.DecodeString(*kcpKey)
			if *kcpKey == "generate" {
				var key *ecdh.PrivateKey
				key, err = ecdh.X25519().GenerateKey(rand.Reader)
				if err == nil {
					raw = key.Bytes()
					log.Printf("generated KCP key (pass -kcp-key=%s to keep it)", base64.StdEncoding.EncodeToString(raw))
				}
			}
			if err != nil {
				log.Fatal("kcp-key: ", err)
			}
			if server.KCPKey, err = ecdh.X25519().NewPrivateKey(raw); err != nil {
				log.Fatal("kcp-key: ", err)
			}
		}
		var err error
		if udp, err = net.ListenPacket("udp", *kcpAddress); err != nil {
			log.Fatal(err)
		}
		if server.KCPKey != nil {
			log.Printf("realtime game server: kcp://%s/ws/game, encrypted; clients need the public key %s", *kcpAddress, base64.StdEncoding.EncodeToString(server.KCPKey.PublicKey().Bytes()))
		} else {
			log.Printf("realtime game server: kcp://%s/ws/game (clear text)", *kcpAddress)
		}
		go func() {
			if err := server.ServeKCP(udp); err != nil {
				log.Fatal(err)
			}
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("/ws/", server)
	// No WriteTimeout: realtime connections are long-lived.
	httpServer := &http.Server{Addr: *address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
		if udp != nil {
			udp.Close() // after Shutdown, so KCP sessions are told and handed off first
		}
		httpServer.Shutdown(shutdown)
	}()
	log.Printf("realtime game server: ws://%s/ws/game", *address)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
