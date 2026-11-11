// Tests for the engine-independent realtime core (TabForgeRealtimeCore.h),
// driven by protocol/realtime/realtime_native_test.go:
//   realtime_test unit  <runtime.json>                    scripted fake server, fake clock
//   realtime_test codec <runtime.json> <cases> <results>  Go/C++ codec equivalence
//   realtime_test chaos <runtime.json> <seconds>          real Go server; the socket
//       layer is bridged over stdin/stdout by the Go test (see bridge protocol below)
#include "TabForgeRealtimeCore.h"
#include <atomic>
#include <cstdio>
#include <fstream>
#include <iostream>
#include <sstream>
#include <thread>

using namespace tabforge::realtime;

namespace {

[[noreturn]] void fail_at(const char* file, int line, const std::string& what) {
    std::cerr << file << ":" << line << ": check failed: " << what << std::endl;
    // _Exit: in chaos mode a thread is blocked reading stdin, and exit()'s
    // stdio cleanup could wait for it.
    std::_Exit(1);
}
#define CHECK(cond, what) do { if (!(cond)) fail_at(__FILE__, __LINE__, std::string(#cond " -- ") + (what)); } while (0)

std::string read_file(const std::string& path) {
    std::ifstream in(path, std::ios::binary);
    std::stringstream ss;
    ss << in.rdbuf();
    return ss.str();
}

Json load_runtime(const std::string& path) {
    Json runtime;
    CHECK(parse_json(read_file(path), runtime, 10000000), "runtime.json parses");
    return runtime;
}

Json obj(std::initializer_list<std::pair<std::string, Json>> members) {
    Json j = Json::object();
    for (const auto& m : members) j.set(m.first, m.second);
    return j;
}

// Go protojson output and ours are equal; numbers compare as doubles, or as
// floats for float32 fields printed with fewer digits.
bool same(const Json& a, const Json& b) {
    if (a.is_number() && b.is_number()) return a.number == b.number || static_cast<float>(a.number) == static_cast<float>(b.number);
    if (a.type != b.type) return false;
    switch (a.type) {
    case Json::Type::Null: return true;
    case Json::Type::Bool: return a.boolean == b.boolean;
    case Json::Type::String: return a.text == b.text;
    case Json::Type::Array:
        if (a.items.size() != b.items.size()) return false;
        for (std::size_t i = 0; i < a.items.size(); ++i) if (!same(a.items[i], b.items[i])) return false;
        return true;
    case Json::Type::Object:
        if (a.members.size() != b.members.size()) return false;
        for (const auto& m : a.members) { const Json* other = b.find(m.first); if (!other || !same(m.second, *other)) return false; }
        return true;
    default: return false;
    }
}

// ------------------------------------------------------------ unit: fake server

struct FakeSocket final : Socket {
    struct Conn {
        std::uint64_t id;
        std::string protocol;
        std::vector<Frame> sent;
        bool closed = false;
    };
    std::vector<Conn> conns;
    std::uint64_t next = 1;
    bool refuse = false, fail_send = false;
    std::uint64_t open(const std::string& subprotocol) override {
        if (refuse) return 0;
        conns.push_back(Conn{next, subprotocol, {}, false});
        return next++;
    }
    bool send(std::uint64_t id, const std::string& data) override {
        Conn* c = find(id);
        CHECK(c && !c->closed, "send on an open socket");
        if (fail_send) return false;
        Frame f;
        std::string error;
        CHECK(Frame::decode(data, f, error), error);
        c->sent.push_back(f);
        return true;
    }
    void close(std::uint64_t id) override { if (Conn* c = find(id)) c->closed = true; }
    Conn* find(std::uint64_t id) { for (auto& c : conns) if (c.id == id) return &c; return nullptr; }
    Conn& last() { CHECK(!conns.empty(), "a socket was opened"); return conns.back(); }
};

struct Rig {
    std::int64_t clock = 1000;
    FakeSocket socket;
    Codec codec;
    Client client;
    std::vector<std::string> states;
    std::vector<std::pair<std::string, std::string>> pushes; // name, payload
    explicit Rig(const Json& runtime, ClientOptions options = {}) : client(socket, [this] { return clock; }) {
        std::string error;
        options.min_backoff_ms = 10;
        options.max_backoff_ms = 20;
        options.seed = 7;
        CHECK(client.configure(runtime, "game", options, error), error);
        const Json* schema = runtime.find("codecSchema");
        CHECK(schema && codec.load(*schema, error), error);
        client.token = [] { return std::string("user-1"); };
        client.on_state = [this](State s, const Error* e) { states.push_back(std::string(state_name(s)) + (e ? ":" + e->code : "")); };
        client.on_push = [this](const Spec& s, const std::string& payload) { pushes.emplace_back(s.name, payload); };
    }
    void advance(std::int64_t ms) { clock += ms; client.tick(); }
    void serve(const Frame& f) { client.received(socket.last().id, f.encode(), false); client.tick(); }
    std::string payload(const std::string& type, const Json& value) {
        std::string out, error;
        CHECK(codec.encode(type, value, out, error), error);
        return out;
    }
    Json decode(const std::string& type, const std::string& data) {
        Json out;
        std::string error;
        CHECK(codec.decode(type, data, out, error), error);
        return out;
    }
    // Opens the current socket and answers its hello with a welcome.
    const Frame& handshake(const std::string& session, bool resumed, std::uint64_t ack = 0) {
        client.opened(socket.last().id, subprotocol_proto);
        client.tick();
        CHECK(!socket.last().sent.empty() && socket.last().sent[0].kind == Hello, "hello first");
        Frame w;
        w.kind = Welcome;
        w.ack = ack;
        w.has_welcome = true;
        w.welcome_session_id = session;
        w.welcome_resume_token = "t-" + session;
        w.welcome_resumed = resumed;
        w.welcome_heartbeat_ms = 5000;
        serve(w);
        return socket.last().sent[0];
    }
    std::string last_state() const { return states.empty() ? "" : states.back(); }
};

Frame response(std::uint64_t seq, std::uint64_t reply_to, std::uint32_t message, std::string payload, std::uint64_t ack) {
    Frame f;
    f.kind = Response; f.seq = seq; f.reply_to = reply_to; f.message = message; f.payload = std::move(payload); f.has_payload = !f.payload.empty(); f.ack = ack;
    return f;
}
Frame push(std::uint64_t seq, std::uint32_t message, std::string payload) {
    Frame f;
    f.kind = Push; f.seq = seq; f.message = message; f.payload = std::move(payload); f.has_payload = !f.payload.empty();
    return f;
}
Frame close_frame(const std::string& code, bool retryable) {
    Frame f;
    f.kind = Close; f.has_error = true; f.error = {code, "bye", retryable};
    return f;
}

struct Result {
    bool done = false;
    Reply reply;
};
std::function<void(const Reply&)> into(Result& r) {
    return [&r](const Reply& reply) { CHECK(!r.done, "a request completes once"); r.done = true; r.reply = reply; };
}

void unit_frames() {
    // The binary welcome used by the C# and TS tests.
    const unsigned char welcome[] = {0x08, 0x02, 0x4a, 0x09, 0x0a, 0x01, 0x73, 0x12, 0x01, 0x74, 0x20, 0x88, 0x27};
    Frame f;
    std::string error;
    CHECK(Frame::decode(std::string(reinterpret_cast<const char*>(welcome), sizeof welcome), f, error), error);
    CHECK(f.validate_from_server(error) && f.kind == Welcome && f.welcome_session_id == "s" && f.welcome_heartbeat_ms == 5000, error);
    // Unknown fields are skipped; extra welcome fields too.
    Frame pong;
    pong.kind = Pong;
    pong.ack = 3;
    std::string data = pong.encode() + std::string("\x78\x05\x82\x01\x02\x01\x02", 7);
    CHECK(Frame::decode(data, f, error) && f.kind == Pong && f.ack == 3 && f.validate_from_server(error), error);
    // Malformed: truncated, wrong wire type, message id range, rule violations.
    CHECK(!Frame::decode(std::string("\x08", 1), f, error), "truncated varint");
    CHECK(!Frame::decode(std::string("\x0a\x00", 2), f, error), "kind as bytes");
    CHECK(!Frame::decode(std::string("\x20\x80\x80\x04", 4), f, error), "message id over 65535");
    Frame bad;
    bad.kind = Push;
    CHECK(Frame::decode(bad.encode(), f, error) && !f.validate_from_server(error), "push without message id");
    bad.kind = Request; bad.seq = 1; bad.message = 1;
    CHECK(Frame::decode(bad.encode(), f, error) && !f.validate_from_server(error), "request from server");
    bad = close_frame("", false);
    CHECK(Frame::decode(bad.encode(), f, error) && !f.validate_from_server(error), "close without code");
    Frame hello;
    hello.kind = Hello;
    hello.hello_version = "1";
    hello.hello_session_id = "s";
    CHECK(Frame::decode(hello.encode(), f, error) && f.has_hello, "hello round trip");
}

void unit_codec(const Json& runtime) {
    Codec codec;
    std::string error, out;
    CHECK(codec.load(*runtime.find("codecSchema"), error), error);
    Json chat = obj({{"messageId", Json::from_string("18446744073709551615")}, {"from", Json::from_string("0")}, {"text", Json::from_string("你好😀")}, {"sentAt", Json::from_string("2024-01-01T08:00:00.5+08:00")}});
    CHECK(codec.encode("tabforge.game.ChatMessage", chat, out, error), error);
    Json back;
    CHECK(codec.decode("tabforge.game.ChatMessage", out, back, error), error);
    CHECK(to_json(back) == "{\"messageId\":\"18446744073709551615\",\"text\":\"你好😀\",\"sentAt\":\"2024-01-01T00:00:00.500Z\"}", to_json(back));
    Json notice = obj({{"kind", Json::from_string("NOTICE_EVENT")}, {"minutesLeft", Json::from_int(0)}});
    CHECK(codec.encode("tabforge.game.ServerNotice", notice, out, error) && codec.decode("tabforge.game.ServerNotice", out, back, error), error);
    CHECK(to_json(back) == "{\"kind\":\"NOTICE_EVENT\",\"minutesLeft\":0}", "oneof presence keeps zero: " + to_json(back));
    // Unknown enum numbers stay numbers; unknown fields are skipped; last scalar wins.
    CHECK(codec.decode("tabforge.game.ServerNotice", std::string("\x08\x07", 2), back, error) && to_json(back) == "{\"kind\":7}", to_json(back));
    CHECK(codec.decode("tabforge.game.Move", std::string("\x78\x01\x10\x03", 4), back, error) && to_json(back) == "{\"tick\":3}", to_json(back));
    CHECK(codec.decode("tabforge.game.Move", std::string("\x10\x01\x10\x02", 4), back, error) && to_json(back) == "{\"tick\":2}", to_json(back));
    CHECK(!codec.decode("tabforge.game.Move", std::string("\x12\x05\x01", 3), back, error), "truncated");
    CHECK(!codec.decode("tabforge.game.Move", std::string("\x12\x01", 2), back, error), "wrong wire type");
    CHECK(!codec.decode("tabforge.game.ChatSend", std::string("\x0a\x01\xff", 3), back, error), "invalid UTF-8");
    CHECK(!codec.encode("tabforge.game.ServerNotice", obj({{"kind", Json::from_string("NOPE")}}), out, error), "unknown enum name");
    CHECK(!codec.encode("tabforge.game.Nope", Json::object(), out, error), "unknown type");
    // Map keys and floats.
    Json buy = obj({{"gold", Json::from_string("-5")}, {"inventory", obj({{"4294967295", Json::from_int(2)}})}});
    CHECK(codec.encode("tabforge.game.BuyReply", buy, out, error) && codec.decode("tabforge.game.BuyReply", out, back, error), error);
    CHECK(to_json(back) == "{\"gold\":\"-5\",\"inventory\":{\"4294967295\":2}}", to_json(back));
    Json move = obj({{"position", obj({{"x", Json::from_double(1.5)}, {"y", Json::from_string("-Infinity")}})}, {"tick", Json::from_int(9)}});
    CHECK(codec.encode("tabforge.game.Move", move, out, error) && codec.decode("tabforge.game.Move", out, back, error), error);
    CHECK(to_json(back) == "{\"position\":{\"x\":1.5,\"y\":\"-Infinity\"},\"tick\":9}", to_json(back));
    std::string raw;
    CHECK(base64_decode("_-8", raw) && raw == "\xff\xef" && base64_encode(raw) == "/+8=", "url-safe base64");
    CHECK(!base64_decode("a", raw) && !base64_decode("ab=c", raw), "bad base64");
}

void unit_session(const Json& runtime) {
    // Handshake, request/response, acknowledgements.
    {
        Rig r(runtime);
        Error error;
        CHECK(r.client.connect(error), error.message);
        CHECK(r.socket.conns.size() == 1 && r.socket.last().protocol == subprotocol_proto, "socket opened with the binary subprotocol");
        const Frame hello = r.handshake("s1", false);
        CHECK(hello.hello_version == runtime.string_or("version") && hello.hello_schema_hash == runtime.string_or("schemaHash") && hello.hello_token == "user-1" && hello.hello_session_id.empty(), "hello");
        CHECK(r.last_state() == "connected" && r.client.session_id() == "s1", r.last_state());
        Result login;
        CHECK(r.client.request("login", r.payload("tabforge.game.LoginRequest", obj({{"nickname", Json::from_string("勇者")}})), 0, into(login), error), error.message);
        const Frame& sent = r.socket.last().sent.back();
        CHECK(sent.kind == Request && sent.seq == 1 && sent.message == 1, "request frame");
        CHECK(r.decode("tabforge.game.LoginRequest", sent.payload).string_or("nickname") == "勇者", "request payload");
        r.serve(response(1, 1, 1, r.payload("tabforge.game.LoginReply", obj({{"playerId", Json::from_string("7")}})), 1));
        CHECK(login.done && login.reply.ok && r.decode("tabforge.game.LoginReply", login.reply.payload).string_or("playerId") == "7", "response");
        CHECK(r.client.unacked() == 0, "server ack releases the request");
        r.advance(60);
        CHECK(r.socket.last().sent.back().kind == AckFrame && r.socket.last().sent.back().ack == 1, "delayed ack");
        // Pushes: duplicates dropped, unknown ids ignored, gap is a protocol error.
        r.serve(push(2, 201, r.payload("tabforge.game.ChatMessage", obj({{"messageId", Json::from_string("1")}}))));
        r.serve(push(2, 201, r.payload("tabforge.game.ChatMessage", obj({{"messageId", Json::from_string("1")}}))));
        r.serve(push(3, 999, ""));
        r.serve(push(0, 200, "")); // volatile push
        CHECK(r.pushes.size() == 2 && r.pushes[0].first == "chatMessage" && r.pushes[1].first == "playerMoved", "pushes");
        // Business error.
        Result chat;
        CHECK(r.client.request("chat", "", 0, into(chat), error), error.message);
        Frame failed = response(4, 2, 3, "", 2);
        failed.has_error = true;
        failed.error = {"empty_message", "say something", false};
        r.serve(failed);
        CHECK(chat.done && !chat.reply.ok && chat.reply.error.code == "empty_message" && chat.reply.error.message == "say something", "business error");
        // Validation of names and kinds.
        CHECK(!r.client.request("move", "", 0, into(chat), error) && error.code == "invalid_message", "notify is not a request");
        CHECK(!r.client.notify("nope", "", error) && error.code == "invalid_message", "unknown notify");
        // Empty messages are ignored; a text frame on the binary subprotocol is a protocol error.
        const std::size_t sockets = r.socket.conns.size();
        r.client.received(r.socket.last().id, "", false);
        r.client.tick();
        CHECK(r.socket.conns.size() == sockets && !r.socket.last().closed, "empty message ignored");
        r.serve(push(6, 201, "")); // seq 5 is missing
        CHECK(r.socket.last().closed && r.last_state() == "disconnected:protocol_error", r.last_state());
        r.advance(30);
        CHECK(r.socket.conns.size() == sockets + 1, "reconnects after backoff");
        const Frame resume = r.handshake("s1", true, 2);
        CHECK(resume.hello_session_id == "s1" && resume.hello_resume_token == "t-s1" && resume.hello_token.empty() && resume.ack == 4, "resume hello");
        CHECK(r.last_state() == "resumed", r.last_state());
        r.client.received(r.socket.last().id, "x", true);
        r.client.tick();
        CHECK(r.last_state() == "disconnected:protocol_error", "text frame: " + r.last_state());
    }
    // Retransmission after resume; requests queued while offline.
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        Result a, b;
        CHECK(r.client.request("buy", r.payload("tabforge.game.BuyRequest", obj({{"itemId", Json::from_int(1)}})), 0, into(a), error), error.message);
        r.client.closed(r.socket.last().id, "");
        r.client.tick();
        CHECK(r.last_state() == "disconnected", r.last_state());
        CHECK(r.client.request("buy", r.payload("tabforge.game.BuyRequest", obj({{"itemId", Json::from_int(2)}})), 0, into(b), error), "queues while offline");
        Error volatile_error;
        CHECK(!r.client.notify("move", "", volatile_error) && volatile_error.code == "dropped", "volatile dropped offline");
        r.advance(30);
        r.handshake("s1", true, 0);
        std::vector<std::uint64_t> resent;
        for (const Frame& f : r.socket.last().sent) if (f.kind == Request) resent.push_back(f.seq);
        CHECK(resent.size() == 2 && resent[0] == 1 && resent[1] == 2, "both requests (re)sent in order with their seqs");
        CHECK(r.client.notify("move", r.payload("tabforge.game.Move", obj({{"tick", Json::from_int(1)}})), error) && r.socket.last().sent.back().seq == 0, "volatile online has no seq");
        r.serve(response(1, 2, 4, "", 2));
        r.serve(response(2, 1, 4, "", 2));
        CHECK(a.done && b.done && a.reply.ok && b.reply.ok, "responses by replyTo");
    }
    // Session expiry: written requests fail with session_lost, unwritten ones move to the new session.
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        Result written, later;
        r.client.request("buy", "", 0, into(written), error);
        r.serve(close_frame("session_expired", true));
        CHECK(written.done && written.reply.error.code == "session_lost", "written request lost");
        CHECK(r.client.request("chat", "", 0, into(later), error), "request while reconnecting");
        r.client.tick();
        const Frame hello = r.handshake("s2", false);
        CHECK(hello.hello_session_id.empty() && hello.hello_token == "user-1", "new session hello");
        CHECK(r.last_state() == "connected" && r.client.session_id() == "s2", r.last_state());
        const Frame& moved = r.socket.last().sent.back();
        CHECK(moved.kind == Request && moved.seq == 1 && moved.message == 3, "unwritten request renumbered into the new session");
        r.serve(response(1, 1, 3, "", 1));
        CHECK(later.done && later.reply.ok, "moved request answered");
    }
    // Timeouts, overload, heartbeat and idle reconnect.
    {
        ClientOptions options;
        options.max_unacked = 2;
        Rig r(runtime, options);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        Result slow, second, third;
        CHECK(r.client.request("chat", "", 100, into(slow), error), error.message);
        CHECK(r.client.request("chat", "", 0, into(second), error), error.message);
        CHECK(!r.client.request("chat", "", 0, into(third), error) && error.code == "overloaded" && error.retryable, "overloaded");
        r.advance(99);
        CHECK(!slow.done, "not yet");
        r.advance(2);
        CHECK(slow.done && slow.reply.error.code == "timeout" && !second.done, "timeout");
        r.serve(response(1, 1, 3, "", 0)); // late answer to the timed-out request is discarded
        const std::size_t sent = r.socket.last().sent.size();
        r.advance(5000);
        bool pinged = false;
        for (std::size_t i = sent; i < r.socket.last().sent.size(); ++i) pinged = pinged || r.socket.last().sent[i].kind == Ping;
        CHECK(pinged, "heartbeat ping");
        const std::size_t sockets = r.socket.conns.size();
        r.advance(6000);
        CHECK(r.socket.conns[sockets - 1].closed && r.last_state() == "disconnected", "idle connection dropped: " + r.last_state());
        r.advance(30);
        CHECK(r.socket.conns.size() == sockets + 1, "reconnect after idle");
    }
    // Permanent closes: close frame, close reason when the frame is lost, client close, wrong subprotocol.
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        Result pending;
        r.client.request("chat", "", 0, into(pending), error);
        r.serve(close_frame("duplicate_login", false));
        CHECK(r.last_state() == "closed:duplicate_login" && pending.done && pending.reply.error.code == "duplicate_login", r.last_state());
        CHECK(!r.client.request("chat", "", 0, into(pending), error) && error.code == "duplicate_login", "closed client rejects requests");
        CHECK(!r.client.connect(error) && error.code == "duplicate_login", "closed client cannot reconnect");
    }
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        r.client.closed(r.socket.last().id, "kicked");
        r.client.tick();
        CHECK(r.last_state() == "closed:kicked", "close reason: " + r.last_state());
    }
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        r.client.closed(r.socket.last().id, "Server Error!"); // not a TabForge code: just a drop
        r.client.tick();
        CHECK(r.last_state() == "disconnected", r.last_state());
    }
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.handshake("s1", false);
        Result pending;
        r.client.request("chat", "", 0, into(pending), error);
        r.client.close();
        CHECK(r.socket.last().sent.back().kind == Close && r.socket.last().sent.back().error.code == "closed" && r.socket.last().closed, "close frame");
        CHECK(pending.done && pending.reply.error.code == "closed" && r.last_state() == "closed:closed", r.last_state());
    }
    {
        Rig r(runtime);
        Error error;
        r.client.connect(error);
        r.client.opened(r.socket.last().id, "other");
        r.client.tick();
        CHECK(r.last_state() == "closed:protocol_error", r.last_state());
    }
    {
        Rig r(runtime);
        Error error;
        r.socket.refuse = true;
        r.client.connect(error);
        CHECK(r.last_state() == "disconnected" && r.socket.conns.empty(), "open refused: " + r.last_state());
        r.socket.refuse = false;
        r.advance(30);
        CHECK(r.socket.conns.size() == 1, "retry after refused open");
        // A failed send drops the connection like a network error.
        r.handshake("s1", false);
        r.socket.fail_send = true;
        Result call;
        r.client.request("chat", "", 0, into(call), error);
        CHECK(r.socket.last().closed && r.last_state() == "disconnected", r.last_state());
        r.socket.fail_send = false;
        r.advance(30);
        r.handshake("s1", true);
        CHECK(r.socket.last().sent.back().kind == Request, "request resent after a failed send");
    }
}

// ------------------------------------------------------------ codec equivalence

void codec_cases(const Json& runtime, const std::string& input, const std::string& output) {
    Codec codec;
    std::string error;
    CHECK(codec.load(*runtime.find("codecSchema"), error), error);
    Json cases;
    CHECK(parse_json(read_file(input), cases, 100000000), "cases parse");
    Json results = Json::array();
    for (const Json& c : cases.items) {
        const std::string type = c.string_or("type");
        Json expected, decoded;
        std::string binary, from_json, from_decoded;
        CHECK(parse_json(c.string_or("json"), expected), "case JSON");
        CHECK(base64_decode(c.string_or("binary"), binary), "case binary");
        CHECK(codec.decode(type, binary, decoded, error), type + ": " + error);
        CHECK(same(decoded, expected), type + " decoded differently\nGo:  " + c.string_or("json") + "\nC++: " + to_json(decoded));
        CHECK(codec.encode(type, expected, from_json, error), type + ": " + error);
        CHECK(codec.encode(type, decoded, from_decoded, error), type + ": " + error);
        results.items.push_back(obj({{"FromJSON", Json::from_string(base64_encode(from_json))}, {"FromDecoded", Json::from_string(base64_encode(from_decoded))}}));
    }
    std::ofstream(output, std::ios::binary) << to_json(results);
    std::cout << "codec: " << cases.items.size() << " messages match Go" << std::endl;
}

// ------------------------------------------------------------ chaos over a bridged socket

// Bridge protocol. Every message is: op (1 byte), socket id (8 bytes LE),
// length (4 bytes LE), data. To the Go test (stdout): 'O' open (data: the
// subprotocol), 'S' send (binary frame), 'C' close, 'L' result line.
// From the Go test (stdin): 'o' opened (data: accepted subprotocol),
// 'm' binary message, 't' text message, 'c' closed (data: close reason).
class BridgeSocket final : public Socket {
public:
    std::mutex out_mutex;
    std::atomic<std::uint64_t> next{1};
    void emit(char op, std::uint64_t id, const std::string& data) {
        std::lock_guard<std::mutex> lock(out_mutex);
        char header[13];
        header[0] = op;
        for (int i = 0; i < 8; ++i) header[1 + i] = static_cast<char>(id >> (8 * i));
        const std::uint32_t n = static_cast<std::uint32_t>(data.size());
        for (int i = 0; i < 4; ++i) header[9 + i] = static_cast<char>(n >> (8 * i));
        std::fwrite(header, 1, sizeof header, stdout);
        std::fwrite(data.data(), 1, data.size(), stdout);
        std::fflush(stdout);
    }
    std::uint64_t open(const std::string& subprotocol) override { const std::uint64_t id = next++; emit('O', id, subprotocol); return id; }
    bool send(std::uint64_t id, const std::string& frame) override { emit('S', id, frame); return true; }
    void close(std::uint64_t id) override { emit('C', id, ""); }
};

void read_events(Client& client, std::atomic<bool>& eof) {
    for (;;) {
        unsigned char header[13];
        if (std::fread(header, 1, sizeof header, stdin) != sizeof header) break;
        std::uint64_t id = 0;
        std::uint32_t n = 0;
        for (int i = 0; i < 8; ++i) id |= std::uint64_t(header[1 + i]) << (8 * i);
        for (int i = 0; i < 4; ++i) n |= std::uint32_t(header[9 + i]) << (8 * i);
        std::string data(n, '\0');
        if (n && std::fread(&data[0], 1, n, stdin) != n) break;
        switch (header[0]) {
        case 'o': client.opened(id, data); break;
        case 'm': client.received(id, std::move(data), false); break;
        case 't': client.received(id, std::move(data), true); break;
        case 'c': client.closed(id, data); break;
        default: fail_at(__FILE__, __LINE__, "unknown bridge event");
        }
    }
    eof = true;
}

void chaos(const Json& runtime, int seconds) {
    BridgeSocket socket;
    Client client(socket);
    Codec codec;
    std::string error;
    ClientOptions options;
    options.min_backoff_ms = 5;
    options.max_backoff_ms = 50;
    CHECK(client.configure(runtime, "game", options, error), error);
    CHECK(codec.load(*runtime.find("codecSchema"), error), error);
    client.token = [] { return std::string("user-31"); };
    std::vector<std::string> states;
    client.on_state = [&](State s, const Error* e) { states.push_back(std::string(state_name(s)) + (e ? ":" + e->code : "")); };
    std::vector<std::uint64_t> pushes;
    Json moved;
    bool has_moved = false;
    client.on_push = [&](const Spec& s, const std::string& payload) {
        Json m;
        std::string why;
        CHECK(codec.decode(s.type, payload, m, why), why);
        if (s.name == "chatMessage") pushes.push_back(std::stoull(m.string_or("messageId")));
        if (s.name == "playerMoved") { moved = m; has_moved = true; }
    };
    std::atomic<bool> eof{false};
    std::thread reader(read_events, std::ref(client), std::ref(eof));
    reader.detach();
    auto encode = [&](const std::string& type, const Json& value) {
        std::string out, why;
        CHECK(codec.encode(type, value, out, why), why);
        return out;
    };
    auto decode = [&](const std::string& type, const std::string& data) {
        Json out;
        std::string why;
        CHECK(codec.decode(type, data, out, why), why);
        return out;
    };
    using steady = std::chrono::steady_clock;
    auto pump_until = [&](const std::function<bool()>& done, int timeout_ms, const std::string& what) {
        const auto deadline = steady::now() + std::chrono::milliseconds(timeout_ms);
        while (!done()) {
            CHECK(!eof, "bridge closed while waiting for " + what);
            CHECK(steady::now() < deadline, "timed out waiting for " + what + " (states: " + (states.empty() ? "" : states.back()) + ")");
            client.tick();
            std::this_thread::sleep_for(std::chrono::milliseconds(1));
        }
    };
    Error err;
    CHECK(client.connect(err), err.message);
    pump_until([&] { return client.state() == State::Connected; }, 15000, "connected");
    const std::string session = client.session_id();

    Result login;
    CHECK(client.request("login", encode("tabforge.game.LoginRequest", obj({{"nickname", Json::from_string("勇者😀")}})), 0, into(login), err), err.message);
    pump_until([&] { return login.done; }, 15000, "login");
    CHECK(login.reply.ok, login.reply.error.code);
    const Json reply = decode("tabforge.game.LoginReply", login.reply.payload);
    CHECK(reply.string_or("playerId") == "31" && reply.string_or("nickname") == "勇者😀" && reply.string_or("gold") == "100", to_json(reply));

    // Purchases under chaos: each must run exactly once.
    std::vector<std::uint64_t> golds;
    const auto end = steady::now() + std::chrono::seconds(seconds);
    int active = 8;
    const std::string buy = encode("tabforge.game.BuyRequest", obj({{"itemId", Json::from_uint(4294967295u)}, {"count", Json::from_int(2)}}));
    std::function<void()> worker = [&] {
        Error e;
        CHECK(client.request("buy", buy, 0, [&](const Reply& r) {
            CHECK(r.ok, "buy failed: " + r.error.code + " " + r.error.message);
            const Json b = decode("tabforge.game.BuyReply", r.payload);
            const Json* inventory = b.find("inventory");
            CHECK(inventory && inventory->find("4294967295") && inventory->find("4294967295")->number == 2, to_json(b));
            golds.push_back(std::stoull(b.string_or("gold")));
            if (steady::now() < end) worker();
            else --active;
        }, e), e.message);
    };
    for (int i = 0; i < 8; ++i) worker();
    pump_until([&] { return active == 0; }, (seconds + 30) * 1000, "purchases");
    std::sort(golds.begin(), golds.end());
    for (std::size_t i = 0; i < golds.size(); ++i) CHECK(golds[i] == i + 1, "a purchase ran twice or was lost at " + std::to_string(i));
    pump_until([&] { return pushes.size() >= golds.size(); }, 10000, "pushes");
    CHECK(pushes.size() == golds.size(), "pushes " + std::to_string(pushes.size()));
    for (std::size_t i = 0; i < pushes.size(); ++i) CHECK(pushes[i] == i + 1, "pushes lost, duplicated or reordered at " + std::to_string(i));
    CHECK(client.session_id() == session, "session was replaced instead of resumed");
    const auto resumes = std::count(states.begin(), states.end(), "resumed");

    // Business error and client deadline.
    Result empty, slow;
    CHECK(client.request("chat", encode("tabforge.game.ChatSend", obj({{"text", Json::from_string("")}})), 0, into(empty), err), err.message);
    CHECK(client.request("chat", encode("tabforge.game.ChatSend", obj({{"text", Json::from_string("slow")}})), 50, into(slow), err), err.message);
    pump_until([&] { return empty.done && slow.done; }, 15000, "chat errors");
    CHECK(!empty.reply.ok && empty.reply.error.code == "empty_message" && empty.reply.error.message == "say something", empty.reply.error.code);
    CHECK(!slow.reply.ok && slow.reply.error.code == "timeout", slow.reply.error.code);

    // Volatile notify -> volatile push; resend like a game loop, frames may be dropped.
    const std::string move = encode("tabforge.game.Move", obj({{"position", obj({{"x", Json::from_double(1.5)}, {"y", Json::from_double(-2)}})}, {"tick", Json::from_int(9)}}));
    auto last_move = steady::now() - std::chrono::seconds(1);
    pump_until([&] {
        if (has_moved) return true;
        if (steady::now() - last_move > std::chrono::milliseconds(20)) { Error ignored; client.notify("move", move, ignored); last_move = steady::now(); }
        return false;
    }, 15000, "volatile push");
    CHECK(moved.number_or("tick") == 9 && moved.find("position")->number_or("x") == 1.5, to_json(moved));

    // A kick closes the client permanently.
    Result kick;
    CHECK(client.request("chat", encode("tabforge.game.ChatSend", obj({{"text", Json::from_string("kick")}})), 0, into(kick), err), err.message);
    pump_until([&] { return client.state() == State::Closed; }, 15000, "kick");
    CHECK(states.back() == "closed:kicked", states.back());
    Result after;
    CHECK(!client.request("chat", "", 0, into(after), err) && err.code == "kicked", err.code);
    socket.emit('L', 0, "{\"requests\":" + std::to_string(golds.size()) + ",\"resumes\":" + std::to_string(resumes) + "}");
    std::fflush(stdout);
    std::_Exit(0); // the reader thread is blocked on stdin
}

} // namespace

int main(int argc, char** argv) {
    if (argc < 3) {
        std::cerr << "usage: realtime_test unit|codec|chaos <runtime.json> ..." << std::endl;
        return 2;
    }
    const std::string mode = argv[1];
    const Json runtime = load_runtime(argv[2]);
    if (mode == "unit") {
        unit_frames();
        unit_codec(runtime);
        unit_session(runtime);
        std::cout << "C++ realtime units passed" << std::endl;
        return 0;
    }
    if (mode == "codec" && argc == 5) { codec_cases(runtime, argv[3], argv[4]); return 0; }
    if (mode == "chaos" && argc == 4) { chaos(runtime, std::atoi(argv[3])); return 0; }
    std::cerr << "bad arguments" << std::endl;
    return 2;
}
