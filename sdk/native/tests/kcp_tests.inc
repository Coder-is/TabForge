// KCP transport tests, included by realtime_test.cpp (which provides Json,
// CHECK, read_file, the bridge framing and the chaos script):
//   kcpcrypto <runtime.json> <cases> <vectors>   crypto against Go-generated cases
//   kcpcore <runtime.json>                       two cores over a lossy virtual link
//   kcpchaos <runtime.json> <url> <key|-> <s>    chaos against ServeKCP; datagrams bridged
//   kcprefuse <runtime.json> <url> <key|-> <text> expects closed:protocol_error mentioning text
// key is the server's X25519 public key in hex, or - for clear text.


namespace kcptest {

using tabforge::kcp::Bytes;
namespace kc = tabforge::kcp::crypto;

inline Bytes hex(const std::string& s) {
    Bytes b(s.size() / 2);
    for (std::size_t i = 0; i < b.size(); ++i) b[i] = static_cast<std::uint8_t>(std::stoi(s.substr(2 * i, 2), nullptr, 16));
    return b;
}

template <typename T>
std::string hex(const T& b) {
    static const char* digits = "0123456789abcdef";
    std::string s;
    for (std::uint8_t v : b) {
        s += digits[v >> 4];
        s += digits[v & 15];
    }
    return s;
}

inline kc::Key key_of(const std::string& s) {
    kc::Key k{};
    const Bytes b = hex(s);
    std::copy(b.begin(), b.begin() + 32, k.begin());
    return k;
}

inline kc::Key random_key() {
    std::random_device rd;
    kc::Key k{};
    for (auto& v : k) v = static_cast<std::uint8_t>(rd());
    return k;
}

inline void crypto_cases(const std::string& cases_path, const std::string& vectors_path) {
    Json cases;
    CHECK(parse_json(read_file(cases_path), cases, 10000000), "cases parse");
    std::size_t xs = 0, aeads = 0;
    for (const Json& c : cases.find("x25519")->items) {
        const Bytes scalar = hex(c.string_or("scalar")), point = hex(c.string_or("point"));
        std::uint8_t out[32];
        CHECK(kc::x25519(scalar.data(), point.data(), out), "x25519 refused " + c.string_or("point"));
        CHECK(hex(Bytes(out, out + 32)) == c.string_or("out"), "x25519 " + c.string_or("scalar"));
        ++xs;
    }
    std::uint8_t zero[32] = {}, out[32];
    CHECK(!kc::x25519(random_key().data(), zero, out), "x25519 accepted a low-order point");
    for (const Json& c : cases.find("aead")->items) {
        const Bytes key = hex(c.string_or("key")), nonce = hex(c.string_or("nonce")), aad = hex(c.string_or("aad")), plain = hex(c.string_or("plain"));
        Bytes sealed;
        kc::seal(key.data(), nonce.data(), aad.data(), aad.size(), plain.data(), plain.size(), sealed);
        CHECK(hex(sealed) == c.string_or("sealed"), "seal " + c.string_or("sealed"));
        Bytes opened;
        CHECK(kc::open(key.data(), nonce.data(), aad.data(), aad.size(), sealed.data(), sealed.size(), opened) && opened == plain, "open");
        for (std::size_t i = 0; i < sealed.size(); i += std::max<std::size_t>(1, sealed.size() / 7)) {
            Bytes bad = sealed;
            bad[i] ^= 1;
            CHECK(!kc::open(key.data(), nonce.data(), aad.data(), aad.size(), bad.data(), bad.size(), opened), "opened a damaged datagram at byte " + std::to_string(i));
        }
        ++aeads;
    }
    std::cout << "kcp crypto: " << xs << " x25519, " << aeads << " aead cases" << std::endl;

    Json v;
    CHECK(parse_json(read_file(vectors_path), v), "vectors parse");
    const std::uint32_t conv = static_cast<std::uint32_t>(v.number_or("conv"));
    const kc::Key server_public = key_of(v.string_or("server_static_public"));
    CHECK(kc::x25519_public(key_of(v.string_or("server_static_private"))) == server_public, "server public key");
    kc::ClientHandshake h(conv, server_public, key_of(v.string_or("client_ephemeral_private")));
    CHECK(hex(h.hello()) == v.string_or("hello"), "hello " + hex(h.hello()));
    const Bytes welcome = hex(v.string_or("welcome"));
    std::unique_ptr<kc::Cipher> client;
    CHECK(h.finish(welcome.data(), welcome.size(), client), "the client rejected the genuine welcome");
    CHECK(hex(client->send_key()) == v.string_or("client_to_server_key") && hex(client->recv_key()) == v.string_or("server_to_client_key"), "derived keys");
    kc::ClientHandshake wrong(conv, kc::x25519_public(random_key()), key_of(v.string_or("client_ephemeral_private")));
    std::unique_ptr<kc::Cipher> none;
    CHECK(!wrong.finish(welcome.data(), welcome.size(), none), "a client pinned to another key accepted the welcome");
    Bytes damaged = welcome;
    damaged[20] ^= 1;
    CHECK(!h.finish(damaged.data(), damaged.size(), none), "accepted a damaged welcome");
    kc::Cipher server(conv, hex(v.string_or("client_to_server_key") + v.string_or("server_to_client_key")), false);
    for (const Json& d : v.find("datagrams")->items) {
        const bool from_client = d.string_or("from") == "client";
        const Bytes plain = hex(d.string_or("plaintext"));
        const Bytes sealed = (from_client ? *client : server).seal(plain.data(), plain.size());
        CHECK(hex(sealed) == d.string_or("sealed"), "sealed " + d.string_or("sealed"));
        Bytes opened;
        CHECK((from_client ? server : *client).open(sealed.data(), sealed.size(), opened) && opened == plain, "opened");
    }
    const std::uint8_t one = 1;
    Bytes zero_counter = client->seal(&one, 1);
    std::fill(zero_counter.begin() + 5, zero_counter.begin() + 13, 0);
    Bytes ignored;
    CHECK(!server.open(zero_counter.data(), zero_counter.size(), ignored), "opened a datagram with counter 0");
    std::cout << "kcp vectors match Go" << std::endl;
}

// Two cores over a link that drops, duplicates and reorders, on a virtual clock.
inline void core_under_loss() {
    std::mt19937 r(7);
    std::uniform_real_distribution<double> unit(0, 1);
    struct Packet { std::uint32_t at; bool to_b; Bytes data; };
    std::vector<Packet> in_flight;
    std::uint32_t now = 0;
    auto link = [&](bool to_b) {
        return [&, to_b](const std::uint8_t* d, std::size_t n) {
            const int copies = unit(r) < 0.05 ? 2 : 1;
            for (int i = 0; i < copies; ++i)
                if (unit(r) >= 0.2) in_flight.push_back({now + 5 + static_cast<std::uint32_t>(r() % 56), to_b, Bytes(d, d + n)});
        };
    };
    tabforge::kcp::Core a(9, link(true)), b(9, link(false));
    for (auto* k : {&a, &b}) {
        k->no_delay(1, 10, 2, true);
        k->wnd_size(256, 256);
        k->set_mtu(1200);
    }
    std::vector<Bytes> sent, got;
    for (int i = 0; i < 300; ++i) {
        Bytes m(1 + r() % 20000);
        for (auto& v : m) v = static_cast<std::uint8_t>(r());
        CHECK(a.send(m.data(), m.size()), "send " + std::to_string(i));
        sent.push_back(std::move(m));
    }
    for (; now < 600000 && got.size() < sent.size(); now += 5) {
        a.update(now);
        b.update(now);
        std::vector<Packet> due;
        for (auto it = in_flight.begin(); it != in_flight.end();) {
            if (it->at <= now) { due.push_back(std::move(*it)); it = in_flight.erase(it); }
            else ++it;
        }
        std::shuffle(due.begin(), due.end(), r);
        for (const Packet& p : due) (p.to_b ? b : a).input(p.data.data(), p.data.size());
        Bytes m;
        while (b.recv(m)) got.push_back(m);
    }
    CHECK(got.size() == sent.size(), "delivered " + std::to_string(got.size()) + " of " + std::to_string(sent.size()));
    for (std::size_t i = 0; i < sent.size(); ++i) CHECK(got[i] == sent[i], "message " + std::to_string(i) + " differs");
    CHECK(!a.dead() && !b.dead(), "dead link under 20% loss");
    std::cout << "kcp core: " << sent.size() << " messages in order after " << now << " virtual ms" << std::endl;
}

// A Socket whose KCP datagrams go through the Go test: 'O' opens a UDP
// socket for an id, 'U' sends a datagram on it, 'C' closes it; the test
// answers 'u' (a received datagram) and 'c' (the UDP socket failed). This
// is the shape of the Unreal client's FSocket layer, with the bridge in
// place of the engine's sockets.
class KcpBridgeSocket final : public Socket {
public:
    Client* client = nullptr;

    KcpBridgeSocket(BridgeSocket& out, std::string url, Bytes server_key) : out_(out), server_key_(std::move(server_key)) {
        std::string host;
        int port = 0;
        CHECK(tabforge::kcp::parse_url(url, host, port, path_), "kcp url " + url);
    }

    std::uint64_t open(const std::string& subprotocol) override {
        const std::uint64_t id = out_.next++;
        out_.emit('O', id, "");
        tabforge::kcp::Conversation::Events events;
        events.opened = [this, id](const std::string& p) { client->opened(id, p); };
        events.received = [this, id](std::string data, bool text) { client->received(id, std::move(data), text); };
        events.refused = [this, id](const std::string& message) { client->refused(id, message); };
        events.closed = [this, id](const std::string& reason) { client->closed(id, reason); };
        std::random_device rd;
        std::uint32_t conv = 0;
        while (conv == 0) conv = rd();
        auto c = std::make_unique<tabforge::kcp::Conversation>(conv, path_, subprotocol, server_key_, random_key(),
            [this, id](const std::uint8_t* d, std::size_t n) { out_.emit('U', id, std::string(reinterpret_cast<const char*>(d), n)); }, events);
        auto* raw = c.get();
        conversations_[id] = std::move(c);
        raw->start(now());
        return id;
    }

    bool send(std::uint64_t id, const std::string& frame) override {
        auto it = conversations_.find(id);
        return it != conversations_.end() && it->second->send(frame, false);
    }

    void close(std::uint64_t id) override {
        auto it = conversations_.find(id);
        if (it != conversations_.end()) it->second->close(now());
    }

    // From the reader thread.
    void incoming(char op, std::uint64_t id, std::string data) {
        std::lock_guard<std::mutex> lock(mutex_);
        inbox_.push_back({op, id, std::move(data)});
    }

    // On the client's thread before each tick, like the engine's ticker.
    void poll() {
        std::deque<Incoming> events;
        {
            std::lock_guard<std::mutex> lock(mutex_);
            events.swap(inbox_);
        }
        const std::int64_t t = now();
        for (Incoming& e : events) {
            auto it = conversations_.find(e.id);
            if (it == conversations_.end()) continue;
            if (e.op == 'u') it->second->input(reinterpret_cast<const std::uint8_t*>(e.data.data()), e.data.size(), t);
            else it->second->abort(); // the UDP socket failed
        }
        for (auto it = conversations_.begin(); it != conversations_.end();) {
            it->second->update(t);
            if (it->second->closed()) {
                out_.emit('C', it->first, "");
                it = conversations_.erase(it);
            } else {
                ++it;
            }
        }
    }

private:
    struct Incoming { char op; std::uint64_t id; std::string data; };
    static std::int64_t now() { return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now().time_since_epoch()).count(); }
    BridgeSocket& out_;
    std::string path_;
    Bytes server_key_;
    std::map<std::uint64_t, std::unique_ptr<tabforge::kcp::Conversation>> conversations_;
    std::mutex mutex_;
    std::deque<Incoming> inbox_;
};

inline void read_datagrams(KcpBridgeSocket& socket, std::atomic<bool>& eof) {
    for (;;) {
        unsigned char header[13];
        if (std::fread(header, 1, sizeof header, stdin) != sizeof header) break;
        std::uint64_t id = 0;
        std::uint32_t n = 0;
        for (int i = 0; i < 8; ++i) id |= std::uint64_t(header[1 + i]) << (8 * i);
        for (int i = 0; i < 4; ++i) n |= std::uint32_t(header[9 + i]) << (8 * i);
        std::string data(n, '\0');
        if (n && std::fread(&data[0], 1, n, stdin) != n) break;
        socket.incoming(static_cast<char>(header[0]), id, std::move(data));
    }
    eof = true;
}

} // namespace kcptest

namespace kcptest {

inline Bytes key_arg(const std::string& arg) { return arg == "-" ? Bytes() : hex(arg); }

// Expects the client to stop with protocol_error mentioning text, without reconnecting.
inline void refuse(const Json& runtime, const std::string& url, const std::string& key, const std::string& text) {
    BridgeSocket bridge;
    KcpBridgeSocket socket(bridge, url, key_arg(key));
    Client client(socket);
    socket.client = &client;
    std::string error;
    ClientOptions options;
    options.min_backoff_ms = 5;
    options.max_backoff_ms = 50;
    CHECK(client.configure(runtime, "game", options, error), error);
    client.token = [] { return std::string("user-5"); };
    std::vector<std::string> states;
    std::string message;
    client.on_state = [&](State s, const Error* e) {
        states.push_back(std::string(state_name(s)) + (e ? ":" + e->code : ""));
        if (e) message = e->message;
    };
    std::atomic<bool> eof{false};
    std::thread reader(read_datagrams, std::ref(socket), std::ref(eof));
    reader.detach();
    Error err;
    CHECK(client.connect(err), err.message);
    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(10);
    while (client.state() != State::Closed && std::chrono::steady_clock::now() < deadline) {
        socket.poll();
        client.tick();
        std::this_thread::sleep_for(std::chrono::milliseconds(1));
    }
    CHECK(client.state() == State::Closed && !states.empty() && states.back() == "closed:protocol_error" && message.find(text) != std::string::npos, "refusal: " + (states.empty() ? "" : states.back()) + " " + message);
    CHECK(std::count(states.begin(), states.end(), "connecting") == 1, "retried a permanent refusal");
    bridge.emit('L', 0, "refused: " + message);
    std::fflush(stdout);
    std::_Exit(0); // the reader thread is blocked on stdin
}

} // namespace kcptest
