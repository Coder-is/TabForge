#pragma once
// KCP transport for the TabForge realtime client (see doc/realtime.md, "KCP
// (UDP) transport"), engine-independent like TabForgeRealtimeCore.h: plain
// C++17 without exceptions, so it builds in Unreal and in the standalone
// sanitizer tests (sdk/native/tests/kcp_test.cpp).
//
//   Core       the KCP protocol, a port of protocol/realtime/internal/kcp
//              (ikcp.c 2.1.1 wire format, IKCP_FASTACK_CONSERVE).
//   crypto     encrypted KCP v1: X25519 (after TweetNaCl), ChaCha20-Poly1305
//              (RFC 8439, Poly1305 after donna-32), SHA-256, HMAC, HKDF. All
//              constant-time; no OpenSSL, so every platform behaves the same.
//   Conversation  one client conversation as a datagram state machine: the
//              encrypted handshake, the open message, 16 KiB chunks,
//              keepalives, resets and refusals. It never touches a socket:
//              the caller feeds datagrams and time in and sends what it emits.

#include <algorithm>
#include <array>
#include <cstdint>
#include <cstring>
#include <deque>
#include <functional>
#include <iterator>
#include <memory>
#include <string>
#include <vector>

namespace tabforge {
namespace kcp {

using Bytes = std::vector<std::uint8_t>;

inline std::uint32_t le32(const std::uint8_t* p) {
    return static_cast<std::uint32_t>(p[0]) | static_cast<std::uint32_t>(p[1]) << 8 | static_cast<std::uint32_t>(p[2]) << 16 | static_cast<std::uint32_t>(p[3]) << 24;
}

inline void put_le32(std::uint8_t* p, std::uint32_t v) {
    p[0] = static_cast<std::uint8_t>(v);
    p[1] = static_cast<std::uint8_t>(v >> 8);
    p[2] = static_cast<std::uint8_t>(v >> 16);
    p[3] = static_cast<std::uint8_t>(v >> 24);
}

inline std::uint64_t le64(const std::uint8_t* p) { return static_cast<std::uint64_t>(le32(p)) | static_cast<std::uint64_t>(le32(p + 4)) << 32; }

inline void put_le64(std::uint8_t* p, std::uint64_t v) {
    put_le32(p, static_cast<std::uint32_t>(v));
    put_le32(p + 4, static_cast<std::uint32_t>(v >> 32));
}

// ---- the KCP protocol ----

class Core {
public:
    static constexpr int kOverhead = 24;
    // Receives each datagram; the bytes are reused after it returns.
    using Output = std::function<void(const std::uint8_t*, std::size_t)>;

    Core(std::uint32_t conv, Output output) : conv_(conv), output_(std::move(output)) { buffer_.reserve((mtu_ + kOverhead) * 3); }

    bool dead() const { return state_ != 0; }
    int wait_snd() const { return static_cast<int>(snd_buf_.size() + snd_queue_.size()); }

    bool set_mtu(int mtu) {
        if (mtu < 50 || mtu < kOverhead) return false;
        mtu_ = static_cast<std::uint32_t>(mtu);
        mss_ = mtu_ - kOverhead;
        return true;
    }

    void wnd_size(int snd, int rcv) {
        if (snd > 0) snd_wnd_ = static_cast<std::uint32_t>(snd);
        if (rcv > 0) rcv_wnd_ = static_cast<std::uint32_t>(std::max(rcv, static_cast<int>(kWndRcv)));
    }

    void no_delay(int nodelay, int interval, int resend, bool nc) {
        if (nodelay >= 0) {
            nodelay_ = static_cast<std::uint32_t>(nodelay);
            rx_minrto_ = nodelay ? kRtoNoDelay : kRtoMin;
        }
        if (interval >= 0) interval_ = static_cast<std::uint32_t>(std::min(std::max(interval, 10), 5000));
        if (resend >= 0) fastresend_ = resend;
        nocwnd_ = nc ? 1 : 0;
    }

    void set_dead_link(int n) { dead_link_ = static_cast<std::uint32_t>(n); }

    int peek_size() const {
        if (rcv_queue_.empty()) return -1;
        const Segment& first = rcv_queue_.front();
        if (first.frg == 0) return static_cast<int>(first.data.size());
        if (rcv_queue_.size() < static_cast<std::size_t>(first.frg) + 1) return -1;
        std::size_t n = 0;
        for (const Segment& s : rcv_queue_) {
            n += s.data.size();
            if (s.frg == 0) break;
        }
        return static_cast<int>(n);
    }

    // The next complete message into out; false if none is ready.
    bool recv(Bytes& out) {
        if (peek_size() < 0) return false;
        const bool recover = rcv_queue_.size() >= rcv_wnd_;
        out.clear();
        while (!rcv_queue_.empty()) {
            Segment seg = std::move(rcv_queue_.front());
            rcv_queue_.pop_front();
            out.insert(out.end(), seg.data.begin(), seg.data.end());
            if (seg.frg == 0) break;
        }
        move_received();
        if (rcv_queue_.size() < rcv_wnd_ && recover) probe_ |= kAskTell;
        return true;
    }

    // Queues a message; false if it needs kWndRcv fragments or more.
    bool send(const std::uint8_t* data, std::size_t size) {
        std::size_t count = size > mss_ ? (size + mss_ - 1) / mss_ : 1;
        if (count >= kWndRcv) return false;
        for (std::size_t i = 0; i < count; ++i) {
            const std::size_t n = std::min<std::size_t>(size, mss_);
            Segment seg;
            seg.data.assign(data, data + n);
            seg.frg = static_cast<std::uint8_t>(count - i - 1);
            snd_queue_.push_back(std::move(seg));
            data += n;
            size -= n;
        }
        return true;
    }

    // Processes one datagram; false if malformed or for another conversation.
    bool input(const std::uint8_t* data, std::size_t size) {
        const std::uint32_t prev_una = snd_una_;
        std::uint32_t maxack = 0, latest_ts = 0;
        bool flag = false;
        if (size < static_cast<std::size_t>(kOverhead)) return false;
        while (size >= static_cast<std::size_t>(kOverhead)) {
            if (le32(data) != conv_) return false;
            const std::uint8_t cmd = data[4], frg = data[5];
            const std::uint16_t wnd = static_cast<std::uint16_t>(data[6] | data[7] << 8);
            const std::uint32_t ts = le32(data + 8), sn = le32(data + 12), una = le32(data + 16), len = le32(data + 20);
            data += kOverhead;
            size -= kOverhead;
            if (size < len) return false;
            if (cmd != kCmdPush && cmd != kCmdAck && cmd != kCmdWask && cmd != kCmdWins) return false;
            rmt_wnd_ = wnd;
            parse_una(una);
            shrink_buf();
            if (cmd == kCmdAck) {
                if (diff(current_, ts) >= 0) update_ack(diff(current_, ts));
                parse_ack(sn);
                shrink_buf();
                if (!flag) {
                    flag = true;
                    maxack = sn;
                    latest_ts = ts;
                } else if (diff(sn, maxack) > 0 && diff(ts, latest_ts) > 0) {
                    maxack = sn;
                    latest_ts = ts;
                }
            } else if (cmd == kCmdPush) {
                if (diff(sn, rcv_nxt_ + rcv_wnd_) < 0) {
                    acklist_.push_back(sn);
                    acklist_.push_back(ts);
                    if (diff(sn, rcv_nxt_) >= 0) {
                        Segment seg;
                        seg.conv = conv_;
                        seg.cmd = cmd;
                        seg.frg = frg;
                        seg.wnd = wnd;
                        seg.ts = ts;
                        seg.sn = sn;
                        seg.una = una;
                        seg.data.assign(data, data + len);
                        parse_data(std::move(seg));
                    }
                }
            } else if (cmd == kCmdWask) {
                probe_ |= kAskTell;
            }
            data += len;
            size -= len;
        }
        if (flag) parse_fastack(maxack, latest_ts);
        if (diff(snd_una_, prev_una) > 0 && cwnd_ < rmt_wnd_) {
            const std::uint32_t mss = mss_;
            if (cwnd_ < ssthresh_) {
                ++cwnd_;
                incr_ += mss;
            } else {
                if (incr_ < mss) incr_ = mss;
                incr_ += mss * mss / incr_ + mss / 16;
                if ((cwnd_ + 1) * mss <= incr_) cwnd_ = (incr_ + mss - 1) / mss;
            }
            if (cwnd_ > rmt_wnd_) {
                cwnd_ = rmt_wnd_;
                incr_ = rmt_wnd_ * mss;
            }
        }
        return true;
    }

    // Sends pending ACKs, probes and due segments now.
    void flush() {
        if (!updated_) return;
        const std::uint32_t now = current_;
        Segment seg;
        seg.conv = conv_;
        seg.cmd = kCmdAck;
        seg.wnd = wnd_unused();
        seg.una = rcv_nxt_;
        for (std::size_t i = 0; i + 1 < acklist_.size(); i += 2) {
            make_space(kOverhead);
            seg.sn = acklist_[i];
            seg.ts = acklist_[i + 1];
            encode(seg);
        }
        acklist_.clear();
        if (rmt_wnd_ == 0) {
            if (probe_wait_ == 0) {
                probe_wait_ = kProbeInit;
                ts_probe_ = now + probe_wait_;
            } else if (diff(now, ts_probe_) >= 0) {
                if (probe_wait_ < kProbeInit) probe_wait_ = kProbeInit;
                probe_wait_ += probe_wait_ / 2;
                if (probe_wait_ > kProbeLimit) probe_wait_ = kProbeLimit;
                ts_probe_ = now + probe_wait_;
                probe_ |= kAskSend;
            }
        } else {
            ts_probe_ = probe_wait_ = 0;
        }
        seg.sn = seg.ts = 0;
        if (probe_ & kAskSend) {
            seg.cmd = kCmdWask;
            make_space(kOverhead);
            encode(seg);
        }
        if (probe_ & kAskTell) {
            seg.cmd = kCmdWins;
            make_space(kOverhead);
            encode(seg);
        }
        probe_ = 0;
        std::uint32_t window = std::min(snd_wnd_, rmt_wnd_);
        if (!nocwnd_) window = std::min(cwnd_, window);
        while (diff(snd_nxt_, snd_una_ + window) < 0 && !snd_queue_.empty()) {
            Segment s = std::move(snd_queue_.front());
            snd_queue_.pop_front();
            s.conv = conv_;
            s.cmd = kCmdPush;
            s.wnd = seg.wnd;
            s.ts = now;
            s.sn = snd_nxt_++;
            s.una = rcv_nxt_;
            s.resendts = now;
            s.rto = static_cast<std::uint32_t>(rx_rto_);
            s.fastack = s.xmit = 0;
            snd_buf_.push_back(std::move(s));
        }
        const std::uint32_t resent = fastresend_ > 0 ? static_cast<std::uint32_t>(fastresend_) : 0xffffffffu;
        const std::uint32_t rtomin = nodelay_ == 0 ? static_cast<std::uint32_t>(rx_rto_ >> 3) : 0;
        bool change = false, lost = false;
        for (Segment& s : snd_buf_) {
            bool needsend = false;
            if (s.xmit == 0) {
                needsend = true;
                ++s.xmit;
                s.rto = static_cast<std::uint32_t>(rx_rto_);
                s.resendts = now + s.rto + rtomin;
            } else if (diff(now, s.resendts) >= 0) {
                needsend = true;
                ++s.xmit;
                if (nodelay_ == 0) s.rto += std::max(s.rto, static_cast<std::uint32_t>(rx_rto_));
                else s.rto += (nodelay_ >= 2 ? static_cast<std::uint32_t>(rx_rto_) : s.rto) / 2;
                s.resendts = now + s.rto;
                lost = true;
            } else if (s.fastack >= resent) {
                if (static_cast<int>(s.xmit) <= kFastackLimit) {
                    needsend = true;
                    ++s.xmit;
                    s.fastack = 0;
                    s.resendts = now + s.rto;
                    change = true;
                }
            }
            if (needsend) {
                s.ts = now;
                s.wnd = seg.wnd;
                s.una = rcv_nxt_;
                make_space(kOverhead + static_cast<int>(s.data.size()));
                encode(s);
                if (s.xmit >= dead_link_) state_ = 0xffffffffu;
            }
        }
        flush_buffer();
        if (change) {
            const std::uint32_t inflight = snd_nxt_ - snd_una_;
            ssthresh_ = std::max(inflight / 2, kThreshMin);
            cwnd_ = ssthresh_ + resent;
            incr_ = cwnd_ * mss_;
        }
        if (lost) {
            ssthresh_ = std::max(window / 2, kThreshMin);
            cwnd_ = 1;
            incr_ = mss_;
        }
        if (cwnd_ < 1) {
            cwnd_ = 1;
            incr_ = mss_;
        }
    }

    // Advances the clock (milliseconds, any origin) and flushes when due.
    void update(std::uint32_t now) {
        current_ = now;
        if (!updated_) {
            updated_ = true;
            ts_flush_ = now;
        }
        std::int32_t slap = diff(now, ts_flush_);
        if (slap >= 10000 || slap < -10000) {
            ts_flush_ = now;
            slap = 0;
        }
        if (slap >= 0) {
            ts_flush_ += interval_;
            if (diff(now, ts_flush_) >= 0) ts_flush_ = now + interval_;
            flush();
        }
    }

private:
    static constexpr std::int32_t kRtoNoDelay = 30, kRtoMin = 100, kRtoDefault = 200, kRtoMax = 60000;
    static constexpr std::uint8_t kCmdPush = 81, kCmdAck = 82, kCmdWask = 83, kCmdWins = 84;
    static constexpr std::uint32_t kAskSend = 1, kAskTell = 2, kWndSnd = 32, kWndRcv = 128;
    static constexpr std::uint32_t kThreshMin = 2, kProbeInit = 7000, kProbeLimit = 120000;
    static constexpr int kFastackLimit = 5;

    struct Segment {
        std::uint32_t conv = 0, ts = 0, sn = 0, una = 0, resendts = 0, rto = 0, fastack = 0, xmit = 0;
        std::uint8_t cmd = 0, frg = 0;
        std::uint16_t wnd = 0;
        Bytes data;
    };

    static std::int32_t diff(std::uint32_t a, std::uint32_t b) { return static_cast<std::int32_t>(a - b); }

    void move_received() {
        while (!rcv_buf_.empty() && rcv_buf_.front().sn == rcv_nxt_ && rcv_queue_.size() < rcv_wnd_) {
            rcv_queue_.push_back(std::move(rcv_buf_.front()));
            rcv_buf_.pop_front();
            ++rcv_nxt_;
        }
    }

    void update_ack(std::int32_t rtt) {
        if (rx_srtt_ == 0) {
            rx_srtt_ = rtt;
            rx_rttval_ = rtt / 2;
        } else {
            const std::int32_t delta = rtt > rx_srtt_ ? rtt - rx_srtt_ : rx_srtt_ - rtt;
            rx_rttval_ = (3 * rx_rttval_ + delta) / 4;
            rx_srtt_ = (7 * rx_srtt_ + rtt) / 8;
            if (rx_srtt_ < 1) rx_srtt_ = 1;
        }
        const std::int32_t rto = rx_srtt_ + std::max(static_cast<std::int32_t>(interval_), 4 * rx_rttval_);
        rx_rto_ = std::min(std::max(rx_minrto_, rto), kRtoMax);
    }

    void shrink_buf() { snd_una_ = snd_buf_.empty() ? snd_nxt_ : snd_buf_.front().sn; }

    void parse_ack(std::uint32_t sn) {
        if (diff(sn, snd_una_) < 0 || diff(sn, snd_nxt_) >= 0) return;
        for (auto it = snd_buf_.begin(); it != snd_buf_.end(); ++it) {
            if (sn == it->sn) {
                snd_buf_.erase(it);
                break;
            }
            if (diff(sn, it->sn) < 0) break;
        }
    }

    void parse_una(std::uint32_t una) {
        while (!snd_buf_.empty() && diff(una, snd_buf_.front().sn) > 0) snd_buf_.pop_front();
    }

    void parse_fastack(std::uint32_t sn, std::uint32_t ts) {
        if (diff(sn, snd_una_) < 0 || diff(sn, snd_nxt_) >= 0) return;
        for (Segment& s : snd_buf_) {
            if (diff(sn, s.sn) < 0) break;
            if (sn != s.sn && diff(ts, s.ts) >= 0) ++s.fastack; // IKCP_FASTACK_CONSERVE
        }
    }

    void parse_data(Segment seg) {
        const std::uint32_t sn = seg.sn;
        if (diff(sn, rcv_nxt_ + rcv_wnd_) >= 0 || diff(sn, rcv_nxt_) < 0) return;
        auto insert = rcv_buf_.end();
        while (insert != rcv_buf_.begin()) {
            auto prev = std::prev(insert);
            if (prev->sn == sn) return; // duplicate
            if (diff(sn, prev->sn) > 0) break;
            insert = prev;
        }
        rcv_buf_.insert(insert, std::move(seg));
        move_received();
    }

    std::uint16_t wnd_unused() const { return rcv_queue_.size() < rcv_wnd_ ? static_cast<std::uint16_t>(rcv_wnd_ - rcv_queue_.size()) : 0; }

    void encode(const Segment& s) {
        std::uint8_t h[kOverhead];
        put_le32(h, s.conv);
        h[4] = s.cmd;
        h[5] = s.frg;
        h[6] = static_cast<std::uint8_t>(s.wnd);
        h[7] = static_cast<std::uint8_t>(s.wnd >> 8);
        put_le32(h + 8, s.ts);
        put_le32(h + 12, s.sn);
        put_le32(h + 16, s.una);
        put_le32(h + 20, static_cast<std::uint32_t>(s.data.size()));
        buffer_.insert(buffer_.end(), h, h + kOverhead);
        buffer_.insert(buffer_.end(), s.data.begin(), s.data.end());
    }

    void make_space(int size) {
        if (buffer_.size() + static_cast<std::size_t>(size) > mtu_) flush_buffer();
    }

    void flush_buffer() {
        if (!buffer_.empty()) {
            output_(buffer_.data(), buffer_.size());
            buffer_.clear();
        }
    }

    std::uint32_t conv_, mtu_ = 1400, mss_ = 1400 - kOverhead, state_ = 0;
    std::uint32_t snd_una_ = 0, snd_nxt_ = 0, rcv_nxt_ = 0, ssthresh_ = 2;
    std::int32_t rx_rttval_ = 0, rx_srtt_ = 0, rx_rto_ = kRtoDefault, rx_minrto_ = kRtoMin;
    std::uint32_t snd_wnd_ = kWndSnd, rcv_wnd_ = kWndRcv, rmt_wnd_ = kWndRcv, cwnd_ = 0, probe_ = 0;
    std::uint32_t current_ = 0, interval_ = 100, ts_flush_ = 100, nodelay_ = 0;
    bool updated_ = false;
    std::uint32_t ts_probe_ = 0, probe_wait_ = 0, dead_link_ = 20, incr_ = 0;
    int fastresend_ = 0, nocwnd_ = 0;
    std::deque<Segment> snd_queue_, rcv_queue_, snd_buf_, rcv_buf_;
    std::vector<std::uint32_t> acklist_;
    Bytes buffer_;
    Output output_;
};

// ---- crypto ----

namespace crypto {

constexpr std::uint8_t kHello = 0xFD, kWelcome = 0xFC, kSealed = 0xFB, kPlainRefuse = 0xFA, kKeepalive = 0xFE, kReset = 0xFF;
constexpr std::size_t kKeySize = 32, kHelloSize = 64, kKeyIdSize = 8, kWelcomeSize = 5 + kKeySize + 16, kSealedHeader = 5 + 8, kSealOverhead = kSealedHeader + 16;

using Key = std::array<std::uint8_t, 32>;

// X25519 (RFC 7748), after TweetNaCl: 16 limbs of 16 bits.
namespace x25519_detail {
using Fe = std::array<std::int64_t, 16>;

inline void car(Fe& o) {
    for (int i = 0; i < 16; ++i) {
        o[i] += std::int64_t(1) << 16;
        const std::int64_t c = o[i] >> 16;
        if (i < 15) o[i + 1] += c - 1;
        else o[0] += 38 * (c - 1);
        o[i] -= c * (std::int64_t(1) << 16);
    }
}

inline void sel(Fe& p, Fe& q, std::int64_t b) {
    const std::int64_t c = ~(b - 1);
    for (int i = 0; i < 16; ++i) {
        const std::int64_t t = c & (p[i] ^ q[i]);
        p[i] ^= t;
        q[i] ^= t;
    }
}

inline void pack(std::uint8_t* o, const Fe& n) {
    Fe m{}, t = n;
    car(t);
    car(t);
    car(t);
    for (int j = 0; j < 2; ++j) {
        m[0] = t[0] - 0xffed;
        for (int i = 1; i < 15; ++i) {
            m[i] = t[i] - 0xffff - ((m[i - 1] >> 16) & 1);
            m[i - 1] &= 0xffff;
        }
        m[15] = t[15] - 0x7fff - ((m[14] >> 16) & 1);
        const std::int64_t b = (m[15] >> 16) & 1;
        m[14] &= 0xffff;
        sel(t, m, 1 - b);
    }
    for (int i = 0; i < 16; ++i) {
        o[2 * i] = static_cast<std::uint8_t>(t[i] & 0xff);
        o[2 * i + 1] = static_cast<std::uint8_t>((t[i] >> 8) & 0xff);
    }
}

inline void unpack(Fe& o, const std::uint8_t* n) {
    for (int i = 0; i < 16; ++i) o[i] = n[2 * i] + (static_cast<std::int64_t>(n[2 * i + 1]) << 8);
    o[15] &= 0x7fff;
}

inline void add(Fe& o, const Fe& a, const Fe& b) { for (int i = 0; i < 16; ++i) o[i] = a[i] + b[i]; }
inline void sub(Fe& o, const Fe& a, const Fe& b) { for (int i = 0; i < 16; ++i) o[i] = a[i] - b[i]; }

inline void mul(Fe& o, const Fe& a, const Fe& b) {
    std::int64_t t[31] = {};
    for (int i = 0; i < 16; ++i)
        for (int j = 0; j < 16; ++j) t[i + j] += a[i] * b[j];
    for (int i = 0; i < 15; ++i) t[i] += 38 * t[i + 16];
    for (int i = 0; i < 16; ++i) o[i] = t[i];
    car(o);
    car(o);
}

inline void inv(Fe& o, const Fe& x) {
    Fe c = x;
    for (int a = 253; a >= 0; --a) {
        mul(c, c, c);
        if (a != 2 && a != 4) mul(c, c, x);
    }
    o = c;
}
} // namespace x25519_detail

// X25519(scalar, point) into out; false for a low-order point (all-zero result).
inline bool x25519(const std::uint8_t* scalar, const std::uint8_t* point, std::uint8_t* out) {
    using namespace x25519_detail;
    std::uint8_t z[32];
    std::memcpy(z, scalar, 32);
    z[31] = static_cast<std::uint8_t>((z[31] & 127) | 64);
    z[0] &= 248;
    Fe x{}, a{}, b{}, c{}, d{}, e{}, f{};
    unpack(x, point);
    b = x;
    a[0] = d[0] = 1;
    const Fe a24{{0xDB41, 1}};
    for (int i = 254; i >= 0; --i) {
        const std::int64_t r = (z[i >> 3] >> (i & 7)) & 1;
        sel(a, b, r);
        sel(c, d, r);
        add(e, a, c);
        sub(a, a, c);
        add(c, b, d);
        sub(b, b, d);
        mul(d, e, e);
        mul(f, a, a);
        mul(a, c, a);
        mul(c, b, e);
        add(e, a, c);
        sub(a, a, c);
        mul(b, a, a);
        sub(c, d, f);
        mul(a, c, a24);
        add(a, a, d);
        mul(c, c, a);
        mul(a, d, f);
        mul(d, b, x);
        mul(b, e, e);
        sel(a, b, r);
        sel(c, d, r);
    }
    inv(c, c);
    mul(a, a, c);
    pack(out, a);
    std::uint8_t any = 0;
    for (int i = 0; i < 32; ++i) any |= out[i];
    return any != 0;
}

inline Key x25519_public(const Key& private_key) {
    std::uint8_t base[32] = {9};
    Key out{};
    x25519(private_key.data(), base, out.data());
    return out;
}

// SHA-256 (FIPS 180-4).
class Sha256 {
public:
    Sha256() { reset(); }
    void reset() {
        static const std::uint32_t init[8] = {0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19};
        std::memcpy(h_, init, sizeof h_);
        length_ = 0;
        used_ = 0;
    }
    void update(const std::uint8_t* p, std::size_t n) {
        length_ += n;
        while (n > 0) {
            const std::size_t take = std::min(n, sizeof block_ - used_);
            std::memcpy(block_ + used_, p, take);
            used_ += take;
            p += take;
            n -= take;
            if (used_ == sizeof block_) {
                compress();
                used_ = 0;
            }
        }
    }
    void update(const Bytes& b) { update(b.data(), b.size()); }
    std::array<std::uint8_t, 32> finish() {
        const std::uint64_t bits = length_ * 8;
        const std::uint8_t one = 0x80, zero = 0;
        update(&one, 1);
        while (used_ != 56) update(&zero, 1);
        std::uint8_t len[8];
        for (int i = 0; i < 8; ++i) len[i] = static_cast<std::uint8_t>(bits >> (56 - 8 * i));
        update(len, 8);
        std::array<std::uint8_t, 32> out{};
        for (int i = 0; i < 8; ++i) {
            out[4 * i] = static_cast<std::uint8_t>(h_[i] >> 24);
            out[4 * i + 1] = static_cast<std::uint8_t>(h_[i] >> 16);
            out[4 * i + 2] = static_cast<std::uint8_t>(h_[i] >> 8);
            out[4 * i + 3] = static_cast<std::uint8_t>(h_[i]);
        }
        return out;
    }

private:
    static std::uint32_t rotr(std::uint32_t v, int n) { return (v >> n) | (v << (32 - n)); }
    void compress() {
        static const std::uint32_t k[64] = {
            0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
            0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
            0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
            0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2};
        std::uint32_t w[64];
        for (int i = 0; i < 16; ++i)
            w[i] = static_cast<std::uint32_t>(block_[4 * i]) << 24 | static_cast<std::uint32_t>(block_[4 * i + 1]) << 16 | static_cast<std::uint32_t>(block_[4 * i + 2]) << 8 | block_[4 * i + 3];
        for (int i = 16; i < 64; ++i) {
            const std::uint32_t s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >> 3);
            const std::uint32_t s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16] + s0 + w[i - 7] + s1;
        }
        std::uint32_t a = h_[0], b = h_[1], c = h_[2], d = h_[3], e = h_[4], f = h_[5], g = h_[6], h = h_[7];
        for (int i = 0; i < 64; ++i) {
            const std::uint32_t t1 = h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + k[i] + w[i];
            const std::uint32_t t2 = (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c));
            h = g;
            g = f;
            f = e;
            e = d + t1;
            d = c;
            c = b;
            b = a;
            a = t1 + t2;
        }
        h_[0] += a; h_[1] += b; h_[2] += c; h_[3] += d; h_[4] += e; h_[5] += f; h_[6] += g; h_[7] += h;
    }
    std::uint32_t h_[8];
    std::uint8_t block_[64];
    std::uint64_t length_;
    std::size_t used_;
};

inline std::array<std::uint8_t, 32> sha256(const Bytes& data) {
    Sha256 h;
    h.update(data);
    return h.finish();
}

inline std::array<std::uint8_t, 32> hmac_sha256(const std::uint8_t* key, std::size_t key_size, const Bytes& data) {
    std::uint8_t k[64] = {};
    if (key_size > 64) {
        Sha256 h;
        h.update(key, key_size);
        const auto d = h.finish();
        std::memcpy(k, d.data(), d.size());
    } else {
        std::memcpy(k, key, key_size);
    }
    std::uint8_t pad[64];
    for (int i = 0; i < 64; ++i) pad[i] = k[i] ^ 0x36;
    Sha256 inner;
    inner.update(pad, 64);
    inner.update(data);
    const auto ih = inner.finish();
    for (int i = 0; i < 64; ++i) pad[i] = k[i] ^ 0x5c;
    Sha256 outer;
    outer.update(pad, 64);
    outer.update(ih.data(), ih.size());
    return outer.finish();
}

// ChaCha20 (RFC 8439).
inline void chacha_block(const std::uint8_t* key, std::uint32_t counter, const std::uint8_t* nonce, std::uint8_t* out) {
    std::uint32_t s[16] = {0x61707865, 0x3320646e, 0x79622d32, 0x6b206574};
    for (int i = 0; i < 8; ++i) s[4 + i] = le32(key + 4 * i);
    s[12] = counter;
    s[13] = le32(nonce);
    s[14] = le32(nonce + 4);
    s[15] = le32(nonce + 8);
    std::uint32_t x[16];
    std::memcpy(x, s, sizeof x);
    auto rotl = [](std::uint32_t v, int n) { return (v << n) | (v >> (32 - n)); };
    auto quarter = [&](int a, int b, int c, int d) {
        x[a] += x[b]; x[d] = rotl(x[d] ^ x[a], 16);
        x[c] += x[d]; x[b] = rotl(x[b] ^ x[c], 12);
        x[a] += x[b]; x[d] = rotl(x[d] ^ x[a], 8);
        x[c] += x[d]; x[b] = rotl(x[b] ^ x[c], 7);
    };
    for (int i = 0; i < 10; ++i) {
        quarter(0, 4, 8, 12); quarter(1, 5, 9, 13); quarter(2, 6, 10, 14); quarter(3, 7, 11, 15);
        quarter(0, 5, 10, 15); quarter(1, 6, 11, 12); quarter(2, 7, 8, 13); quarter(3, 4, 9, 14);
    }
    for (int i = 0; i < 16; ++i) put_le32(out + 4 * i, x[i] + s[i]);
}

inline void chacha_xor(const std::uint8_t* key, const std::uint8_t* nonce, const std::uint8_t* in, std::size_t n, std::uint8_t* out) {
    std::uint8_t block[64];
    std::uint32_t counter = 1;
    for (std::size_t done = 0; done < n; done += 64, ++counter) {
        chacha_block(key, counter, nonce, block);
        const std::size_t m = std::min<std::size_t>(64, n - done);
        for (std::size_t i = 0; i < m; ++i) out[done + i] = in[done + i] ^ block[i];
    }
}

// Poly1305 (RFC 8439), after poly1305-donna 32-bit.
inline void poly1305(const std::uint8_t* key, const std::uint8_t* m, std::size_t n, std::uint8_t* tag) {
    const std::uint32_t r0 = le32(key) & 0x3ffffff, r1 = (le32(key + 3) >> 2) & 0x3ffff03, r2 = (le32(key + 6) >> 4) & 0x3ffc0ff, r3 = (le32(key + 9) >> 6) & 0x3f03fff, r4 = (le32(key + 12) >> 8) & 0x00fffff;
    const std::uint32_t s1 = r1 * 5, s2 = r2 * 5, s3 = r3 * 5, s4 = r4 * 5;
    std::uint32_t h0 = 0, h1 = 0, h2 = 0, h3 = 0, h4 = 0;
    std::uint8_t block[16];
    for (std::size_t at = 0; at < n; at += 16) {
        const std::size_t len = std::min<std::size_t>(16, n - at);
        std::uint32_t hibit = 1u << 24;
        std::memcpy(block, m + at, len);
        if (len < 16) {
            block[len] = 1;
            std::memset(block + len + 1, 0, 16 - len - 1);
            hibit = 0;
        }
        h0 += le32(block) & 0x3ffffff;
        h1 += (le32(block + 3) >> 2) & 0x3ffffff;
        h2 += (le32(block + 6) >> 4) & 0x3ffffff;
        h3 += (le32(block + 9) >> 6) & 0x3ffffff;
        h4 += (le32(block + 12) >> 8) | hibit;
        std::uint64_t d0 = std::uint64_t(h0) * r0 + std::uint64_t(h1) * s4 + std::uint64_t(h2) * s3 + std::uint64_t(h3) * s2 + std::uint64_t(h4) * s1;
        std::uint64_t d1 = std::uint64_t(h0) * r1 + std::uint64_t(h1) * r0 + std::uint64_t(h2) * s4 + std::uint64_t(h3) * s3 + std::uint64_t(h4) * s2;
        std::uint64_t d2 = std::uint64_t(h0) * r2 + std::uint64_t(h1) * r1 + std::uint64_t(h2) * r0 + std::uint64_t(h3) * s4 + std::uint64_t(h4) * s3;
        std::uint64_t d3 = std::uint64_t(h0) * r3 + std::uint64_t(h1) * r2 + std::uint64_t(h2) * r1 + std::uint64_t(h3) * r0 + std::uint64_t(h4) * s4;
        std::uint64_t d4 = std::uint64_t(h0) * r4 + std::uint64_t(h1) * r3 + std::uint64_t(h2) * r2 + std::uint64_t(h3) * r1 + std::uint64_t(h4) * r0;
        std::uint32_t c = static_cast<std::uint32_t>(d0 >> 26); h0 = static_cast<std::uint32_t>(d0) & 0x3ffffff;
        d1 += c; c = static_cast<std::uint32_t>(d1 >> 26); h1 = static_cast<std::uint32_t>(d1) & 0x3ffffff;
        d2 += c; c = static_cast<std::uint32_t>(d2 >> 26); h2 = static_cast<std::uint32_t>(d2) & 0x3ffffff;
        d3 += c; c = static_cast<std::uint32_t>(d3 >> 26); h3 = static_cast<std::uint32_t>(d3) & 0x3ffffff;
        d4 += c; c = static_cast<std::uint32_t>(d4 >> 26); h4 = static_cast<std::uint32_t>(d4) & 0x3ffffff;
        h0 += c * 5; c = h0 >> 26; h0 &= 0x3ffffff;
        h1 += c;
    }
    std::uint32_t c = h1 >> 26; h1 &= 0x3ffffff;
    h2 += c; c = h2 >> 26; h2 &= 0x3ffffff;
    h3 += c; c = h3 >> 26; h3 &= 0x3ffffff;
    h4 += c; c = h4 >> 26; h4 &= 0x3ffffff;
    h0 += c * 5; c = h0 >> 26; h0 &= 0x3ffffff;
    h1 += c;
    std::uint32_t g0 = h0 + 5; c = g0 >> 26; g0 &= 0x3ffffff;
    std::uint32_t g1 = h1 + c; c = g1 >> 26; g1 &= 0x3ffffff;
    std::uint32_t g2 = h2 + c; c = g2 >> 26; g2 &= 0x3ffffff;
    std::uint32_t g3 = h3 + c; c = g3 >> 26; g3 &= 0x3ffffff;
    std::uint32_t g4 = h4 + c - (1u << 26);
    std::uint32_t mask = (g4 >> 31) - 1;
    g0 &= mask; g1 &= mask; g2 &= mask; g3 &= mask; g4 &= mask;
    mask = ~mask;
    h0 = (h0 & mask) | g0; h1 = (h1 & mask) | g1; h2 = (h2 & mask) | g2; h3 = (h3 & mask) | g3; h4 = (h4 & mask) | g4;
    h0 = h0 | (h1 << 26);
    h1 = (h1 >> 6) | (h2 << 20);
    h2 = (h2 >> 12) | (h3 << 14);
    h3 = (h3 >> 18) | (h4 << 8);
    std::uint64_t f = std::uint64_t(h0) + le32(key + 16); h0 = static_cast<std::uint32_t>(f);
    f = std::uint64_t(h1) + le32(key + 20) + (f >> 32); h1 = static_cast<std::uint32_t>(f);
    f = std::uint64_t(h2) + le32(key + 24) + (f >> 32); h2 = static_cast<std::uint32_t>(f);
    f = std::uint64_t(h3) + le32(key + 28) + (f >> 32); h3 = static_cast<std::uint32_t>(f);
    put_le32(tag, h0);
    put_le32(tag + 4, h1);
    put_le32(tag + 8, h2);
    put_le32(tag + 12, h3);
}

// AEAD_CHACHA20_POLY1305 (RFC 8439 2.8).
inline void aead_tag(const std::uint8_t* key, const std::uint8_t* nonce, const std::uint8_t* aad, std::size_t aad_n, const std::uint8_t* ct, std::size_t ct_n, std::uint8_t* tag) {
    std::uint8_t block[64];
    chacha_block(key, 0, nonce, block);
    const std::size_t pa = (aad_n + 15) & ~std::size_t(15), pc = (ct_n + 15) & ~std::size_t(15);
    Bytes mac(pa + pc + 16, 0);
    if (aad_n) std::memcpy(mac.data(), aad, aad_n);
    if (ct_n) std::memcpy(mac.data() + pa, ct, ct_n);
    put_le64(mac.data() + pa + pc, aad_n);
    put_le64(mac.data() + pa + pc + 8, ct_n);
    poly1305(block, mac.data(), mac.size(), tag);
}

// Appends ciphertext and the 16-byte tag of plain to out.
inline void seal(const std::uint8_t* key, const std::uint8_t* nonce, const std::uint8_t* aad, std::size_t aad_n, const std::uint8_t* plain, std::size_t n, Bytes& out) {
    const std::size_t at = out.size();
    out.resize(at + n + 16);
    if (n) chacha_xor(key, nonce, plain, n, out.data() + at);
    aead_tag(key, nonce, aad, aad_n, out.data() + at, n, out.data() + at + n);
}

// The plaintext of ciphertext||tag into plain; false if it does not authenticate.
inline bool open(const std::uint8_t* key, const std::uint8_t* nonce, const std::uint8_t* aad, std::size_t aad_n, const std::uint8_t* sealed, std::size_t n, Bytes& plain) {
    if (n < 16) return false;
    const std::size_t ct_n = n - 16;
    std::uint8_t tag[16];
    aead_tag(key, nonce, aad, aad_n, sealed, ct_n, tag);
    std::uint8_t diff = 0;
    for (int i = 0; i < 16; ++i) diff |= tag[i] ^ sealed[ct_n + i];
    if (diff != 0) return false;
    plain.resize(ct_n);
    if (ct_n) chacha_xor(key, nonce, sealed, ct_n, plain.data());
    return true;
}

inline std::array<std::uint8_t, 12> nonce(std::uint64_t counter) {
    std::array<std::uint8_t, 12> n{};
    put_le64(n.data() + 4, counter);
    return n;
}

// Client-to-server key followed by server-to-client key (kcpKeyMaterial in Go).
inline Bytes key_material(std::uint32_t conv, const Key& client_eph, const Key& server_eph, const Key& server_static, const Key& ee, const Key& es) {
    Bytes transcript;
    const char* label = "tabforge-kcp-v1";
    transcript.insert(transcript.end(), label, label + std::strlen(label));
    std::uint8_t c[4];
    put_le32(c, conv);
    transcript.insert(transcript.end(), c, c + 4);
    for (const Key* k : {&client_eph, &server_eph, &server_static}) transcript.insert(transcript.end(), k->begin(), k->end());
    const auto salt = sha256(transcript);
    Bytes ikm(ee.begin(), ee.end());
    ikm.insert(ikm.end(), es.begin(), es.end());
    const auto prk = hmac_sha256(salt.data(), salt.size(), ikm);
    Bytes t1_in = {'k', 'e', 'y', 's', 1};
    const auto t1 = hmac_sha256(prk.data(), prk.size(), t1_in);
    Bytes t2_in(t1.begin(), t1.end());
    t2_in.insert(t2_in.end(), {'k', 'e', 'y', 's', 2});
    const auto t2 = hmac_sha256(prk.data(), prk.size(), t2_in);
    Bytes out(t1.begin(), t1.end());
    out.insert(out.end(), t2.begin(), t2.end());
    return out;
}

// First 8 bytes of SHA-256(server public key): lets a rotating server pick the key.
inline Bytes key_id(const Key& server_public) {
    const auto h = sha256(Bytes(server_public.begin(), server_public.end()));
    return Bytes(h.begin(), h.begin() + kKeyIdSize);
}

inline Bytes hello_datagram(std::uint32_t conv, const Key& client_eph_public, const Key& server_public) {
    Bytes b(kHelloSize, 0);
    put_le32(b.data(), conv);
    b[4] = kHello;
    b[5] = 1;
    std::memcpy(b.data() + 6, client_eph_public.data(), kKeySize);
    const Bytes id = key_id(server_public);
    std::memcpy(b.data() + 6 + kKeySize, id.data(), kKeyIdSize);
    return b;
}

// Seals and opens the datagrams of one encrypted conversation.
class Cipher {
public:
    Cipher(std::uint32_t conv, const Bytes& keys, bool client) : conv_(conv) {
        std::memcpy((client ? send_ : recv_).data(), keys.data(), 32);
        std::memcpy((client ? recv_ : send_).data(), keys.data() + 32, 32);
    }
    const Key& send_key() const { return send_; }
    const Key& recv_key() const { return recv_; }

    // conv | 0xFB | counter | ChaCha20-Poly1305(plain), the header as associated data.
    Bytes seal(const std::uint8_t* plain, std::size_t n) {
        const std::uint64_t counter = ++sent_;
        Bytes out(kSealedHeader);
        put_le32(out.data(), conv_);
        out[4] = kSealed;
        put_le64(out.data() + 5, counter);
        const auto nc = nonce(counter);
        const std::array<std::uint8_t, kSealedHeader> header = header_of(out);
        crypto::seal(send_.data(), nc.data(), header.data(), header.size(), plain, n, out);
        return out;
    }

    // The plaintext of a sealed datagram; false if it does not open.
    bool open(const std::uint8_t* data, std::size_t n, Bytes& plain) const {
        if (n < kSealOverhead || data[4] != kSealed) return false;
        const std::uint64_t counter = le64(data + 5);
        if (counter == 0) return false; // reserved for the welcome
        const auto nc = nonce(counter);
        return crypto::open(recv_.data(), nc.data(), data, kSealedHeader, data + kSealedHeader, n - kSealedHeader, plain);
    }

private:
    static std::array<std::uint8_t, kSealedHeader> header_of(const Bytes& b) {
        std::array<std::uint8_t, kSealedHeader> h{};
        std::memcpy(h.data(), b.data(), kSealedHeader);
        return h;
    }
    std::uint32_t conv_;
    Key send_{}, recv_{};
    std::uint64_t sent_ = 0; // last counter used; 0 is the welcome's
};

// The client's half of the handshake, kept until a welcome arrives.
class ClientHandshake {
public:
    ClientHandshake(std::uint32_t conv, const Key& server_public, const Key& ephemeral_private)
        : conv_(conv), server_(server_public), eph_private_(ephemeral_private), eph_public_(x25519_public(ephemeral_private)), hello_(hello_datagram(conv, eph_public_, server_public)) {}

    const Bytes& hello() const { return hello_; }

    // Builds the cipher into out if data is this conversation's genuine welcome.
    bool finish(const std::uint8_t* data, std::size_t n, std::unique_ptr<Cipher>& out) const {
        if (n != kWelcomeSize || data[4] != kWelcome || le32(data) != conv_) return false;
        Key server_eph{}, ee{}, es{};
        std::memcpy(server_eph.data(), data + 5, kKeySize);
        if (!x25519(eph_private_.data(), server_eph.data(), ee.data()) || !x25519(eph_private_.data(), server_.data(), es.data())) return false;
        auto cipher = std::make_unique<Cipher>(conv_, key_material(conv_, eph_public_, server_eph, server_, ee, es), true);
        Bytes plain;
        const auto nc = nonce(0);
        if (!crypto::open(cipher->recv_key().data(), nc.data(), data, 5 + kKeySize, data + 5 + kKeySize, 16, plain)) return false;
        out = std::move(cipher);
        return true;
    }

private:
    std::uint32_t conv_;
    Key server_, eph_private_, eph_public_;
    Bytes hello_;
};

} // namespace crypto

// ---- one client conversation ----

// Parses kcp://host:port/path; false if url is not one.
inline bool parse_url(const std::string& url, std::string& host, int& port, std::string& path) {
    const std::string scheme = "kcp://";
    if (url.compare(0, scheme.size(), scheme) != 0) return false;
    const std::string rest = url.substr(scheme.size());
    const std::size_t slash = rest.find('/');
    const std::string authority = rest.substr(0, slash);
    path = slash == std::string::npos ? "/" : rest.substr(slash);
    std::size_t colon = authority.rfind(':');
    if (colon == std::string::npos || colon + 1 == authority.size()) return false;
    host = authority.substr(0, colon);
    if (host.size() > 1 && host.front() == '[' && host.back() == ']') host = host.substr(1, host.size() - 2);
    port = 0;
    for (std::size_t i = colon + 1; i < authority.size(); ++i) {
        if (authority[i] < '0' || authority[i] > '9' || port > 65535) return false;
        port = port * 10 + (authority[i] - '0');
    }
    return !host.empty() && port > 0 && port <= 65535;
}

// One client conversation, driven by the caller: feed received datagrams to
// input(), call update() every few milliseconds, send what send_datagram
// emits. Events fire synchronously from those calls. Not thread-safe.
class Conversation {
public:
    struct Events {
        std::function<void(const std::string& subprotocol)> opened;
        std::function<void(std::string data, bool text)> received;
        // The conversation ended; reason is the server's close reason ("" if none).
        std::function<void(const std::string& reason)> closed;
        // A refusal reconnecting cannot fix; closed follows.
        std::function<void(const std::string& message)> refused;
    };
    static constexpr std::size_t kMaxMessage = 1 << 20;

    // server_key empty: clear text. conv must be random and non-zero;
    // ephemeral_private random (32 bytes) when server_key is set.
    Conversation(std::uint32_t conv, std::string path, std::string subprotocol, Bytes server_key, crypto::Key ephemeral_private,
                 std::function<void(const std::uint8_t*, std::size_t)> send_datagram, Events events)
        : conv_(conv), path_(std::move(path)), subprotocol_(std::move(subprotocol)), server_key_(std::move(server_key)), eph_(ephemeral_private),
          send_(std::move(send_datagram)), events_(std::move(events)) {}

    bool closed() const { return closed_; }
    bool is_open() const { return opened_ && !closed_ && !closing_; }

    void start(std::int64_t now) {
        if (server_key_.empty()) {
            start_kcp(now);
            return;
        }
        if (server_key_.size() != crypto::kKeySize) {
            refuse("The KCP server key is a 32-byte X25519 public key");
            return;
        }
        crypto::Key server{};
        std::memcpy(server.data(), server_key_.data(), crypto::kKeySize);
        handshake_ = std::make_unique<crypto::ClientHandshake>(conv_, server, eph_);
        handshake_deadline_ = now + kHandshakeMs;
        send_hello(now);
    }

    void input(const std::uint8_t* data, std::size_t n, std::int64_t now) {
        if (closed_ || n < 5 || le32(data) != conv_) return;
        const bool reset = n == 5 && data[4] == crypto::kReset;
        if (handshake_) {
            if (reset) {
                // A plain server resets every hello; one or two may be damage or forgery.
                if (++resets_ >= kHandshakeResets) refuse("The server does not offer encrypted KCP; clear the KCP server key or set Server.KCPKey");
                else send_hello(now);
                return;
            }
            if (handshake_->finish(data, n, cipher_)) {
                handshake_.reset();
                start_kcp(now);
            }
            return;
        }
        if (reset) {
            finish("");
            return;
        }
        Bytes plain;
        const std::uint8_t* p = data;
        std::size_t size = n;
        if (!cipher_) {
            if (n == 5 && data[4] == crypto::kKeepalive) return;
            if (n == 5 && data[4] == crypto::kPlainRefuse) {
                refuse("The server only accepts encrypted KCP; set the KCP server key");
                return;
            }
        } else {
            if (!cipher_->open(data, n, plain)) return; // Damaged or forged: dropped like a lost datagram.
            last_recv_ = now;
            if (plain.empty()) return; // Keepalive.
            p = plain.data();
            size = plain.size();
        }
        last_recv_ = now;
        if (!kcp_->input(p, size)) return;
        Bytes m;
        while (!closed_ && kcp_->recv(m)) deliver(m);
        if (!closed_) kcp_->flush(); // Acknowledge at once instead of on the next tick.
    }

    void update(std::int64_t now) {
        if (closed_) return;
        if (handshake_) {
            if (now >= handshake_deadline_) finish(""); // Retried like a network failure.
            else if (now >= hello_at_) send_hello(now);
            return;
        }
        kcp_->update(static_cast<std::uint32_t>(now));
        if (kcp_->dead()) {
            finish("");
            return;
        }
        if (now - last_recv_ > kQuietMs && now - last_alive_ > kQuietMs) {
            last_alive_ = now;
            if (cipher_) {
                const Bytes b = cipher_->seal(nullptr, 0);
                send_(b.data(), b.size());
            } else {
                std::uint8_t b[5];
                put_le32(b, conv_);
                b[4] = crypto::kKeepalive;
                send_(b, 5);
            }
        }
        if (closing_ && (kcp_->wait_snd() == 0 || now >= close_deadline_)) finish("");
    }

    bool send(const std::string& data, bool text) {
        if (!is_open() || data.size() > kMaxMessage) return false;
        queue(text ? kText : kBinary, reinterpret_cast<const std::uint8_t*>(data.data()), data.size());
        return true;
    }

    // Ends the conversation now, for example when its socket failed; closed fires.
    void abort() { finish(""); }

    // Sends what is queued and a close message; closed fires after it is
    // acknowledged or after one second (keep calling update()).
    void close(std::int64_t now) {
        if (closing_ || closed_) return;
        closing_ = true;
        close_deadline_ = now + kCloseLingerMs;
        if (!kcp_ || !opened_) {
            finish("");
            return;
        }
        queue(kClose, nullptr, 0);
    }

private:
    static constexpr std::uint8_t kOpen = 0, kBinary = 1, kText = 2, kClose = 3, kMore = 0x80;
    static constexpr std::size_t kChunk = 16 << 10;
    static constexpr int kMtu = 1200, kWindow = 256, kIntervalMs = 10, kDeadLink = 40, kHandshakeResets = 3;
    static constexpr std::int64_t kQuietMs = 1000, kHelloRetryMs = 250, kHandshakeMs = 10000, kCloseLingerMs = 1000;

    void send_hello(std::int64_t now) {
        send_(handshake_->hello().data(), handshake_->hello().size());
        hello_at_ = now + kHelloRetryMs;
    }

    void start_kcp(std::int64_t now) {
        kcp_ = std::make_unique<Core>(conv_, [this](const std::uint8_t* d, std::size_t n) {
            if (cipher_) {
                const Bytes b = cipher_->seal(d, n);
                send_(b.data(), b.size());
            } else {
                send_(d, n);
            }
        });
        kcp_->no_delay(1, kIntervalMs, 2, true);
        kcp_->wnd_size(kWindow, kWindow);
        kcp_->set_mtu(cipher_ ? kMtu - static_cast<int>(crypto::kSealOverhead) : kMtu);
        kcp_->set_dead_link(kDeadLink);
        kcp_->update(static_cast<std::uint32_t>(now));
        last_recv_ = last_alive_ = now;
        const std::string open = path_ + "\n" + subprotocol_;
        queue(kOpen, reinterpret_cast<const std::uint8_t*>(open.data()), open.size());
    }

    void queue(std::uint8_t type, const std::uint8_t* p, std::size_t n) {
        std::size_t at = 0;
        do {
            const std::size_t m = std::min(n - at, kChunk);
            Bytes chunk(1 + m);
            chunk[0] = static_cast<std::uint8_t>(type | (at + m < n ? kMore : 0));
            if (m) std::memcpy(chunk.data() + 1, p + at, m);
            kcp_->send(chunk.data(), chunk.size());
            at += m;
        } while (at < n);
        kcp_->flush();
    }

    void deliver(const Bytes& m) {
        if (m.empty()) {
            finish("");
            return;
        }
        const std::uint8_t type = m[0] & static_cast<std::uint8_t>(~kMore);
        const bool more = (m[0] & kMore) != 0;
        const std::string body(m.begin() + 1, m.end());
        switch (type) {
        case kOpen:
            if (opened_ || body != subprotocol_) {
                finish("");
                return;
            }
            opened_ = true;
            if (events_.opened) events_.opened(subprotocol_);
            return;
        case kClose:
            if (!opened_ && body.compare(0, 6, "offer ") == 0) {
                refuse("Server did not accept " + subprotocol_);
                return;
            }
            finish(body);
            return;
        case kBinary:
        case kText:
            if (!opened_ || (partial_type_ >= 0 && partial_type_ != type) || partial_.size() + body.size() > kMaxMessage) {
                finish("");
                return;
            }
            partial_type_ = type;
            partial_ += body;
            if (!more) {
                std::string message;
                message.swap(partial_);
                partial_type_ = -1;
                if (events_.received) events_.received(std::move(message), type == kText);
            }
            return;
        default:
            finish("");
        }
    }

    void refuse(const std::string& message) {
        if (closed_) return;
        if (events_.refused) events_.refused(message);
        finish("");
    }

    void finish(const std::string& reason) {
        if (closed_) return;
        closed_ = true;
        if (events_.closed) events_.closed(reason);
    }

    std::uint32_t conv_;
    std::string path_, subprotocol_;
    Bytes server_key_;
    crypto::Key eph_;
    std::function<void(const std::uint8_t*, std::size_t)> send_;
    Events events_;
    std::unique_ptr<crypto::ClientHandshake> handshake_;
    std::unique_ptr<crypto::Cipher> cipher_;
    std::unique_ptr<Core> kcp_;
    std::int64_t hello_at_ = 0, handshake_deadline_ = 0, last_recv_ = 0, last_alive_ = 0, close_deadline_ = 0;
    int resets_ = 0;
    bool opened_ = false, closing_ = false, closed_ = false;
    std::string partial_;
    int partial_type_ = -1;
};

} // namespace kcp
} // namespace tabforge
