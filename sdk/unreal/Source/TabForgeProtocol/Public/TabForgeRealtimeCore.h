#pragma once
// Engine-independent core of the TabForge realtime channel client (see
// doc/realtime.md): a small JSON tree, the ProtoJSON <-> Protobuf payload
// codec driven by runtime.json's codecSchema, the binary Frame codec and the
// session engine (handshake, sequencing, acknowledgements, resume and
// retransmission, heartbeats, reconnect backoff, request deadlines).
//
// Plain C++17 without exceptions or engine types, so it builds in Unreal and
// in the standalone sanitizer tests (sdk/native/tests/realtime_test.cpp). The
// Unreal client (TabForgeRealtime.h) adds IWebSocket, FJsonValue and the
// game-thread ticker on top.
#include "TabForgeJsonSyntax.h"
#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <deque>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <random>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

namespace tabforge {
namespace realtime {

// ---------------------------------------------------------------- JSON tree

// A JSON value. Objects keep member order; numbers keep their source text
// when parsed (or when produced from an integer) so 64-bit values stay exact.
struct Json {
    enum class Type { Null, Bool, Number, String, Array, Object };
    Type type = Type::Null;
    bool boolean = false;
    double number = 0;
    std::string text; // string value, or the source text of a number ("" if none)
    std::vector<Json> items;
    std::vector<std::pair<std::string, Json>> members;

    static Json null() { return Json(); }
    static Json from_bool(bool v) { Json j; j.type = Type::Bool; j.boolean = v; return j; }
    static Json from_string(std::string v) { Json j; j.type = Type::String; j.text = std::move(v); return j; }
    static Json from_double(double v) { Json j; j.type = Type::Number; j.number = v; return j; }
    static Json from_int(std::int64_t v) { Json j; j.type = Type::Number; j.number = static_cast<double>(v); j.text = std::to_string(v); return j; }
    static Json from_uint(std::uint64_t v) { Json j; j.type = Type::Number; j.number = static_cast<double>(v); j.text = std::to_string(v); return j; }
    static Json object() { Json j; j.type = Type::Object; return j; }
    static Json array() { Json j; j.type = Type::Array; return j; }

    bool is_null() const { return type == Type::Null; }
    bool is_object() const { return type == Type::Object; }
    bool is_array() const { return type == Type::Array; }
    bool is_string() const { return type == Type::String; }
    bool is_number() const { return type == Type::Number; }
    bool is_bool() const { return type == Type::Bool; }

    const Json* find(const std::string& name) const {
        for (const auto& m : members) if (m.first == name) return &m.second;
        return nullptr;
    }
    Json* find(const std::string& name) {
        for (auto& m : members) if (m.first == name) return &m.second;
        return nullptr;
    }
    Json& set(const std::string& name, Json value) {
        if (Json* existing = find(name)) { *existing = std::move(value); return *existing; }
        members.emplace_back(name, std::move(value));
        return members.back().second;
    }
    void erase(const std::string& name) {
        members.erase(std::remove_if(members.begin(), members.end(), [&](const std::pair<std::string, Json>& m) { return m.first == name; }), members.end());
    }
    std::string string_or(const std::string& name, const std::string& fallback = "") const {
        const Json* v = find(name);
        return v && v->is_string() ? v->text : fallback;
    }
    bool bool_or(const std::string& name, bool fallback = false) const {
        const Json* v = find(name);
        return v && v->is_bool() ? v->boolean : fallback;
    }
    double number_or(const std::string& name, double fallback = 0) const {
        const Json* v = find(name);
        return v && v->is_number() ? v->number : fallback;
    }
};

namespace detail {
class JsonBuilder final : public JsonVisitor {
public:
    Json root;
    std::vector<Json> stack;
    std::vector<std::string> keys;
    bool add(Json value) {
        if (stack.empty()) { root = std::move(value); return true; }
        Json& top = stack.back();
        if (top.is_object()) { top.members.emplace_back(std::move(keys.back()), std::move(value)); keys.pop_back(); }
        else top.items.push_back(std::move(value));
        return true;
    }
    bool begin_object() override { stack.push_back(Json::object()); return true; }
    bool key(const std::string& name) override { keys.push_back(name); return true; }
    bool end_object() override { Json done = std::move(stack.back()); stack.pop_back(); return add(std::move(done)); }
    bool begin_array() override { stack.push_back(Json::array()); return true; }
    bool end_array() override { Json done = std::move(stack.back()); stack.pop_back(); return add(std::move(done)); }
    bool string_value(const std::string& value) override { return add(Json::from_string(value)); }
    bool number_value(const std::string& text) override {
        Json j; j.type = Json::Type::Number; j.text = text; j.number = std::strtod(text.c_str(), nullptr);
        return add(std::move(j));
    }
    bool bool_value(bool value) override { return add(Json::from_bool(value)); }
    bool null_value() override { return add(Json()); }
};

inline void quote(std::string& out, const std::string& s) {
    out += '"';
    for (unsigned char c : s) {
        switch (c) {
        case '"': out += "\\\""; break;
        case '\\': out += "\\\\"; break;
        case '\n': out += "\\n"; break;
        case '\r': out += "\\r"; break;
        case '\t': out += "\\t"; break;
        case '\b': out += "\\b"; break;
        case '\f': out += "\\f"; break;
        default:
            if (c < 0x20) { char buf[8]; std::snprintf(buf, sizeof buf, "\\u%04x", c); out += buf; }
            else out += static_cast<char>(c);
        }
    }
    out += '"';
}

inline std::string format_double(double v) {
    if (std::isnan(v) || std::isinf(v)) return "null"; // not representable in JSON
    if (v == std::floor(v) && std::fabs(v) < 1e15) { char buf[32]; std::snprintf(buf, sizeof buf, "%.0f", v); return buf; }
    char buf[40];
    for (int precision = 15; precision <= 17; ++precision) {
        std::snprintf(buf, sizeof buf, "%.*g", precision, v);
        if (std::strtod(buf, nullptr) == v) break;
    }
    return buf;
}

inline void write(std::string& out, const Json& j) {
    switch (j.type) {
    case Json::Type::Null: out += "null"; return;
    case Json::Type::Bool: out += j.boolean ? "true" : "false"; return;
    case Json::Type::Number: out += j.text.empty() ? format_double(j.number) : j.text; return;
    case Json::Type::String: quote(out, j.text); return;
    case Json::Type::Array:
        out += '[';
        for (std::size_t i = 0; i < j.items.size(); ++i) { if (i) out += ','; write(out, j.items[i]); }
        out += ']';
        return;
    case Json::Type::Object:
        out += '{';
        for (std::size_t i = 0; i < j.members.size(); ++i) {
            if (i) out += ',';
            quote(out, j.members[i].first);
            out += ':';
            write(out, j.members[i].second);
        }
        out += '}';
        return;
    }
}
} // namespace detail

// Strict parse (UTF-8, no duplicate members, bounded depth and size).
inline bool parse_json(const std::string& text, Json& out, std::size_t max_nodes = protocol_max_nodes) {
    detail::JsonBuilder builder;
    if (!JsonSyntax(text, json_max_depth, max_nodes, &builder).valid()) return false;
    out = std::move(builder.root);
    return true;
}

inline std::string to_json(const Json& value) {
    std::string out;
    detail::write(out, value);
    return out;
}

// ---------------------------------------------------------------- Base64

inline std::string base64_encode(const std::string& data) {
    static const char alphabet[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    std::string out;
    out.reserve((data.size() + 2) / 3 * 4);
    std::size_t i = 0;
    for (; i + 2 < data.size(); i += 3) {
        const std::uint32_t n = (std::uint32_t(std::uint8_t(data[i])) << 16) | (std::uint32_t(std::uint8_t(data[i + 1])) << 8) | std::uint8_t(data[i + 2]);
        out += alphabet[(n >> 18) & 63]; out += alphabet[(n >> 12) & 63]; out += alphabet[(n >> 6) & 63]; out += alphabet[n & 63];
    }
    if (i + 1 == data.size()) {
        const std::uint32_t n = std::uint32_t(std::uint8_t(data[i])) << 16;
        out += alphabet[(n >> 18) & 63]; out += alphabet[(n >> 12) & 63]; out += "==";
    } else if (i + 2 == data.size()) {
        const std::uint32_t n = (std::uint32_t(std::uint8_t(data[i])) << 16) | (std::uint32_t(std::uint8_t(data[i + 1])) << 8);
        out += alphabet[(n >> 18) & 63]; out += alphabet[(n >> 12) & 63]; out += alphabet[(n >> 6) & 63]; out += '=';
    }
    return out;
}

// Accepts standard and URL-safe alphabets, with or without padding (as protojson does).
inline bool base64_decode(const std::string& text, std::string& out) {
    out.clear();
    std::size_t length = text.size();
    while (length > 0 && text[length - 1] == '=') --length;
    if (text.size() - length > 2 || (text.size() != length && text.size() % 4 != 0) || length % 4 == 1) return false;
    std::uint32_t buffer = 0;
    int bits = 0;
    for (std::size_t i = 0; i < length; ++i) {
        const char c = text[i];
        int v;
        if (c >= 'A' && c <= 'Z') v = c - 'A';
        else if (c >= 'a' && c <= 'z') v = c - 'a' + 26;
        else if (c >= '0' && c <= '9') v = c - '0' + 52;
        else if (c == '+' || c == '-') v = 62;
        else if (c == '/' || c == '_') v = 63;
        else return false;
        buffer = (buffer << 6) | std::uint32_t(v);
        bits += 6;
        if (bits >= 8) { bits -= 8; out += static_cast<char>((buffer >> bits) & 0xff); }
    }
    return true;
}

// ---------------------------------------------------------------- Protobuf wire

namespace wire {
enum : int { Varint = 0, I64 = 1, Len = 2, I32 = 5 };

struct Writer {
    std::string out;
    void byte(std::uint8_t b) { out += static_cast<char>(b); }
    void varint(std::uint64_t v) { while (v > 0x7f) { byte(static_cast<std::uint8_t>(v | 0x80)); v >>= 7; } byte(static_cast<std::uint8_t>(v)); }
    void tag(int no, int type) { varint((std::uint64_t(no) << 3) | std::uint64_t(type)); }
    void fixed32(std::uint32_t v) { for (int i = 0; i < 4; ++i) byte(static_cast<std::uint8_t>(v >> (8 * i))); }
    void fixed64(std::uint64_t v) { for (int i = 0; i < 8; ++i) byte(static_cast<std::uint8_t>(v >> (8 * i))); }
    void bytes(const std::string& data) { varint(data.size()); out += data; }
};

struct Reader {
    const std::string* data;
    std::size_t pos, end;
    std::string error;
    Reader(const std::string& d, std::size_t start, std::size_t stop) : data(&d), pos(start), end(stop) {}
    bool fail(const char* why) { if (error.empty()) error = why; return false; }
    bool done() const { return pos >= end; }
    bool varint(std::uint64_t& v) {
        v = 0;
        for (int i = 0; i < 10; ++i) {
            if (pos >= end) return fail("truncated varint");
            const std::uint8_t b = static_cast<std::uint8_t>((*data)[pos++]);
            if (i == 9 && b > 1) return fail("varint overflows 64 bits");
            v |= std::uint64_t(b & 0x7f) << (7 * i);
            if (b < 0x80) return true;
        }
        return fail("varint too long");
    }
    bool advance(std::uint64_t n) { if (n > end - pos) return fail("truncated field"); pos += static_cast<std::size_t>(n); return true; }
    bool fixed32(std::uint32_t& v) {
        if (!advance(4)) return false;
        v = 0; for (int i = 0; i < 4; ++i) v |= std::uint32_t(std::uint8_t((*data)[pos - 4 + i])) << (8 * i);
        return true;
    }
    bool fixed64(std::uint64_t& v) {
        if (!advance(8)) return false;
        v = 0; for (int i = 0; i < 8; ++i) v |= std::uint64_t(std::uint8_t((*data)[pos - 8 + i])) << (8 * i);
        return true;
    }
    bool length(std::size_t& start, std::size_t& stop) {
        std::uint64_t n;
        if (!varint(n)) return false;
        start = pos;
        if (!advance(n)) return false;
        stop = pos;
        return true;
    }
    bool skip(int type, int depth) {
        std::uint64_t ignored; std::size_t a, b;
        switch (type) {
        case Varint: return varint(ignored);
        case I64: return advance(8);
        case Len: return length(a, b);
        case I32: return advance(4);
        case 3:
            if (depth > 100) return fail("message too deep");
            for (;;) {
                std::uint64_t tag;
                if (!varint(tag)) return false;
                if ((tag & 7) == 4) return true;
                if (!skip(static_cast<int>(tag & 7), depth + 1)) return false;
            }
        default: return fail("invalid wire type");
        }
    }
};

inline bool valid_utf8(const std::string& s, std::size_t start, std::size_t stop) {
    return tabforge::valid_utf8(s.substr(start, stop - start));
}
} // namespace wire

// ---------------------------------------------------------------- payload codec

// ProtoJSON (canonical, as produced by the wire validator with normalize) to
// binary Protobuf and back, driven by codecSchema. Decoding produces exactly
// what Go protojson.Marshal would: 64-bit integers as decimal strings, enums
// by name, bytes as Base64, default values omitted.
class Codec {
public:
    bool load(const Json& schema, std::string& error) {
        const Json* types = schema.find("messages");
        const Json* enums = schema.find("enums");
        if (!types || !types->is_object() || !enums || !enums->is_object()) { error = "Load codecSchema from the generated runtime.json"; return false; }
        for (const auto& type : types->members) {
            if (!type.second.is_array()) { error = "codecSchema: bad message " + type.first; return false; }
            Message& m = messages_[type.first];
            for (const Json& f : type.second.items) {
                Field field;
                field.no = static_cast<int>(f.number_or("no"));
                field.name = f.string_or("name");
                field.kind = f.string_or("kind");
                field.type = f.string_or("type");
                field.map_key = f.string_or("mapKey");
                field.list = f.bool_or("list");
                field.packed = f.bool_or("packed");
                field.presence = f.bool_or("presence");
                field.oneof = static_cast<int>(f.number_or("oneof"));
                if (field.no <= 0 || field.name.empty() || field.kind.empty()) { error = "codecSchema: bad field in " + type.first; return false; }
                m.by_number[field.no] = m.fields.size();
                m.fields.push_back(field);
            }
        }
        for (const auto& e : enums->members) {
            auto& numbers = enum_numbers_[e.first];
            auto& names = enum_names_[e.first];
            for (const Json& pair : e.second.items) {
                if (!pair.is_array() || pair.items.size() != 2 || !pair.items[0].is_string() || !pair.items[1].is_number()) { error = "codecSchema: bad enum " + e.first; return false; }
                const std::int32_t number = static_cast<std::int32_t>(pair.items[1].number);
                numbers[pair.items[0].text] = number;
                names.emplace(number, pair.items[0].text); // the first name of an alias wins
            }
        }
        return true;
    }

    bool has_message(const std::string& type) const { return messages_.count(type) != 0; }

    bool encode(const std::string& type, const Json& value, std::string& out, std::string& error) const {
        wire::Writer w;
        error.clear();
        if (!write_message(w, type, value, 0, error)) return false;
        out = std::move(w.out);
        return true;
    }

    bool decode(const std::string& type, const std::string& data, Json& out, std::string& error) const {
        error.clear();
        out = Json::object();
        wire::Reader r(data, 0, data.size());
        if (!read_message(type, r, 0, out, error)) {
            if (error.empty()) error = r.error.empty() ? "invalid message" : r.error;
            return false;
        }
        return true;
    }

private:
    struct Field {
        int no = 0;
        std::string name, kind, type, map_key;
        bool list = false, packed = false, presence = false;
        int oneof = 0;
    };
    struct Message {
        std::vector<Field> fields;
        std::unordered_map<int, std::size_t> by_number;
    };
    std::unordered_map<std::string, Message> messages_;
    std::unordered_map<std::string, std::unordered_map<std::string, std::int32_t>> enum_numbers_;
    std::unordered_map<std::string, std::map<std::int32_t, std::string>> enum_names_;

    static constexpr int max_depth = 100;

    static bool fail(std::string& error, const std::string& why) { if (error.empty()) error = why; return false; }

    static int wire_type(const std::string& kind) {
        if (kind == "fixed64" || kind == "sfixed64" || kind == "double") return wire::I64;
        if (kind == "fixed32" || kind == "sfixed32" || kind == "float") return wire::I32;
        if (kind == "string" || kind == "bytes" || kind == "message") return wire::Len;
        return wire::Varint;
    }
    static bool long_kind(const std::string& k) { return k == "int64" || k == "uint64" || k == "sint64" || k == "fixed64" || k == "sfixed64"; }
    static bool packable(const std::string& k) { return k != "string" && k != "bytes" && k != "message"; }
    static const char* wrapper_kind(const std::string& type) {
        static const std::pair<const char*, const char*> wrappers[] = {
            {"google.protobuf.DoubleValue", "double"}, {"google.protobuf.FloatValue", "float"}, {"google.protobuf.Int64Value", "int64"},
            {"google.protobuf.UInt64Value", "uint64"}, {"google.protobuf.Int32Value", "int32"}, {"google.protobuf.UInt32Value", "uint32"},
            {"google.protobuf.BoolValue", "bool"}, {"google.protobuf.StringValue", "string"}, {"google.protobuf.BytesValue", "bytes"}};
        for (const auto& w : wrappers) if (type == w.first) return w.second;
        return nullptr;
    }
    static bool is_special(const std::string& type) {
        return type == "google.protobuf.Timestamp" || type == "google.protobuf.Duration" || type == "google.protobuf.Empty" || wrapper_kind(type) != nullptr;
    }

    const Message* message(const std::string& type, std::string& error) const {
        auto it = messages_.find(type);
        if (it == messages_.end()) { fail(error, "unknown message type " + type); return nullptr; }
        return &it->second;
    }

    // ---- numbers from canonical ProtoJSON ----

    static bool parse_int(const Json& v, std::int64_t& out) {
        if (v.is_number()) {
            if (!v.text.empty() && v.text.find_first_of(".eE") == std::string::npos) {
                errno = 0; char* end = nullptr;
                out = std::strtoll(v.text.c_str(), &end, 10);
                return errno == 0 && end && *end == 0;
            }
            if (v.number != std::floor(v.number) || std::fabs(v.number) > 9.2e18) return false;
            out = static_cast<std::int64_t>(v.number);
            return true;
        }
        if (!v.is_string() || v.text.empty()) return false;
        errno = 0; char* end = nullptr;
        out = std::strtoll(v.text.c_str(), &end, 10);
        return errno == 0 && end && *end == 0;
    }
    static bool parse_uint(const Json& v, std::uint64_t& out) {
        const std::string* text = nullptr;
        if (v.is_string()) text = &v.text;
        else if (v.is_number() && !v.text.empty() && v.text.find_first_of(".eE") == std::string::npos) text = &v.text;
        if (text) {
            if (text->empty() || (*text)[0] == '-') return false;
            errno = 0; char* end = nullptr;
            out = std::strtoull(text->c_str(), &end, 10);
            return errno == 0 && end && *end == 0;
        }
        if (!v.is_number() || v.number < 0 || v.number != std::floor(v.number) || v.number >= 18446744073709551616.0) return false;
        out = static_cast<std::uint64_t>(v.number);
        return true;
    }
    static bool parse_real(const Json& v, double& out) {
        if (v.is_string()) {
            if (v.text == "NaN") { out = std::nan(""); return true; }
            if (v.text == "Infinity") { out = HUGE_VAL; return true; }
            if (v.text == "-Infinity") { out = -HUGE_VAL; return true; }
            char* end = nullptr;
            out = std::strtod(v.text.c_str(), &end);
            return !v.text.empty() && end && *end == 0;
        }
        if (!v.is_number()) return false;
        out = v.number;
        return true;
    }

    bool is_default(const Field& field, const Json& value) const {
        if (field.kind == "string" || field.kind == "bytes") return value.is_string() && value.text.empty();
        if (field.kind == "bool") return value.is_bool() && !value.boolean;
        if (field.kind == "enum") {
            if (value.is_number()) return value.number == 0;
            if (!value.is_string()) return false;
            auto e = enum_numbers_.find(field.type);
            if (e == enum_numbers_.end()) return false;
            auto n = e->second.find(value.text);
            return n != e->second.end() && n->second == 0;
        }
        if (value.is_string()) return value.text == "0";
        if (value.is_number()) return value.number == 0 && !std::signbit(value.number);
        return false;
    }

    // ---- encoding ----

    bool write_message(wire::Writer& w, const std::string& type, const Json& input, int depth, std::string& error) const {
        if (depth > max_depth) return fail(error, "message too deep");
        Json converted;
        const Json* value = &input;
        if (type.compare(0, 16, "google.protobuf.") == 0) {
            if (!from_special_json(type, input, converted, error)) return false;
            value = &converted;
        }
        if (!value->is_object()) return fail(error, type + ": expected an object");
        const Message* m = message(type, error);
        if (!m) return false;
        for (const Field& field : m->fields) {
            const Json* item = value->find(field.name);
            if (!item || item->is_null()) continue;
            if (!field.map_key.empty()) {
                if (!item->is_object()) return fail(error, field.name + ": expected a map");
                for (const auto& entry : item->members) {
                    wire::Writer body;
                    Json key;
                    if (field.map_key == "bool") key = Json::from_bool(entry.first == "true");
                    else if (field.map_key == "string" || long_kind(field.map_key)) key = Json::from_string(entry.first);
                    else { key.type = Json::Type::Number; key.text = entry.first; key.number = std::strtod(entry.first.c_str(), nullptr); }
                    if (!write_scalar(body, 1, field.map_key, "", key, depth + 1, error)) return false;
                    if (!write_scalar(body, 2, field.kind, field.type, entry.second, depth + 1, error)) return false;
                    w.tag(field.no, wire::Len);
                    w.bytes(body.out);
                }
            } else if (field.list) {
                if (!item->is_array()) return fail(error, field.name + ": expected an array");
                if (item->items.empty()) continue;
                if (field.packed && packable(field.kind)) {
                    wire::Writer body;
                    for (const Json& element : item->items)
                        if (!write_value(body, field.kind, field.type, element, depth + 1, error)) return false;
                    w.tag(field.no, wire::Len);
                    w.bytes(body.out);
                } else {
                    for (const Json& element : item->items)
                        if (!write_scalar(w, field.no, field.kind, field.type, element, depth + 1, error)) return false;
                }
            } else if (field.presence || field.kind == "message" || !is_default(field, *item)) {
                if (!write_scalar(w, field.no, field.kind, field.type, *item, depth + 1, error)) return false;
            }
        }
        return true;
    }

    bool write_scalar(wire::Writer& w, int no, const std::string& kind, const std::string& type, const Json& value, int depth, std::string& error) const {
        w.tag(no, wire_type(kind));
        return write_value(w, kind, type, value, depth, error);
    }

    bool write_value(wire::Writer& w, const std::string& kind, const std::string& type, const Json& value, int depth, std::string& error) const {
        const std::string bad = "invalid " + kind + " value";
        if (kind == "message") {
            wire::Writer body;
            if (!write_message(body, type, value, depth, error)) return false;
            w.bytes(body.out);
            return true;
        }
        if (kind == "string") { if (!value.is_string()) return fail(error, bad); w.bytes(value.text); return true; }
        if (kind == "bytes") {
            std::string raw;
            if (!value.is_string() || !base64_decode(value.text, raw)) return fail(error, bad);
            w.bytes(raw);
            return true;
        }
        if (kind == "bool") { if (!value.is_bool()) return fail(error, bad); w.byte(value.boolean ? 1 : 0); return true; }
        if (kind == "enum") {
            std::int64_t number;
            if (value.is_number()) { if (!parse_int(value, number)) return fail(error, bad); }
            else {
                auto e = enum_numbers_.find(type);
                if (!value.is_string() || e == enum_numbers_.end() || !e->second.count(value.text)) return fail(error, "unknown enum value " + value.text);
                number = e->second.at(value.text);
            }
            w.varint(static_cast<std::uint64_t>(number));
            return true;
        }
        if (kind == "int32" || kind == "int64") { std::int64_t n; if (!parse_int(value, n)) return fail(error, bad); w.varint(static_cast<std::uint64_t>(n)); return true; }
        if (kind == "uint32" || kind == "uint64") { std::uint64_t n; if (!parse_uint(value, n)) return fail(error, bad); w.varint(n); return true; }
        if (kind == "sint32") {
            std::int64_t n; if (!parse_int(value, n)) return fail(error, bad);
            const std::int32_t v = static_cast<std::int32_t>(n);
            w.varint(static_cast<std::uint32_t>((static_cast<std::uint32_t>(v) << 1) ^ static_cast<std::uint32_t>(v >> 31)));
            return true;
        }
        if (kind == "sint64") {
            std::int64_t n; if (!parse_int(value, n)) return fail(error, bad);
            w.varint((static_cast<std::uint64_t>(n) << 1) ^ static_cast<std::uint64_t>(n >> 63));
            return true;
        }
        if (kind == "fixed32") { std::uint64_t n; if (!parse_uint(value, n)) return fail(error, bad); w.fixed32(static_cast<std::uint32_t>(n)); return true; }
        if (kind == "sfixed32") { std::int64_t n; if (!parse_int(value, n)) return fail(error, bad); w.fixed32(static_cast<std::uint32_t>(static_cast<std::int32_t>(n))); return true; }
        if (kind == "fixed64") { std::uint64_t n; if (!parse_uint(value, n)) return fail(error, bad); w.fixed64(n); return true; }
        if (kind == "sfixed64") { std::int64_t n; if (!parse_int(value, n)) return fail(error, bad); w.fixed64(static_cast<std::uint64_t>(n)); return true; }
        if (kind == "float") {
            double d; if (!parse_real(value, d)) return fail(error, bad);
            const float f = static_cast<float>(d);
            std::uint32_t bits; std::memcpy(&bits, &f, 4);
            w.fixed32(bits);
            return true;
        }
        if (kind == "double") {
            double d; if (!parse_real(value, d)) return fail(error, bad);
            std::uint64_t bits; std::memcpy(&bits, &d, 8);
            w.fixed64(bits);
            return true;
        }
        return fail(error, "unsupported kind " + kind);
    }

    bool from_special_json(const std::string& type, const Json& value, Json& out, std::string& error) const {
        if (type == "google.protobuf.Timestamp") {
            std::int64_t seconds; std::int32_t nanos;
            if (!value.is_string() || !parse_timestamp(value.text, seconds, nanos)) return fail(error, "invalid timestamp");
            out = Json::object();
            out.set("seconds", Json::from_string(std::to_string(seconds)));
            out.set("nanos", Json::from_int(nanos));
            return true;
        }
        if (type == "google.protobuf.Duration") {
            std::int64_t seconds; std::int32_t nanos;
            if (!value.is_string() || !parse_duration(value.text, seconds, nanos)) return fail(error, "invalid duration");
            out = Json::object();
            out.set("seconds", Json::from_string(std::to_string(seconds)));
            out.set("nanos", Json::from_int(nanos));
            return true;
        }
        if (wrapper_kind(type)) { out = Json::object(); out.set("value", value); return true; }
        out = value;
        return true;
    }

    // ---- decoding ----

    bool read_message(const std::string& type, wire::Reader& r, int depth, Json& into, std::string& error) const {
        if (depth > max_depth) return fail(error, "message too deep");
        const Message* m = message(type, error);
        if (!m) return false;
        while (!r.done()) {
            std::uint64_t tag;
            if (!r.varint(tag)) return fail(error, r.error);
            const std::uint64_t no = tag >> 3;
            const int type_bits = static_cast<int>(tag & 7);
            if (no == 0 || no > 0x7fffffff) return fail(error, "invalid field number");
            auto found = m->by_number.find(static_cast<int>(no));
            if (found == m->by_number.end()) {
                if (!r.skip(type_bits, depth)) return fail(error, r.error);
                continue;
            }
            const Field& field = m->fields[found->second];
            if (!field.map_key.empty()) {
                if (type_bits != wire::Len) return fail(error, "wrong wire type");
                std::size_t start, stop;
                if (!r.length(start, stop)) return fail(error, r.error);
                wire::Reader entry(*r.data, start, stop);
                Json key, value;
                bool has_key = false, has_value = false;
                while (!entry.done()) {
                    std::uint64_t entry_tag;
                    if (!entry.varint(entry_tag)) return fail(error, entry.error);
                    const int entry_wire = static_cast<int>(entry_tag & 7);
                    if ((entry_tag >> 3) == 1) { if (!read_value(entry, entry_wire, field.map_key, "", depth + 1, key, error)) return false; has_key = true; }
                    else if ((entry_tag >> 3) == 2) { if (!read_value(entry, entry_wire, field.kind, field.type, depth + 1, value, error)) return false; has_value = true; }
                    else if (!entry.skip(entry_wire, depth)) return fail(error, entry.error);
                }
                std::string key_text;
                if (!has_key) key_text = field.map_key == "bool" ? "false" : field.map_key == "string" ? "" : "0";
                else if (key.is_bool()) key_text = key.boolean ? "true" : "false";
                else if (key.is_string()) key_text = key.text;
                else key_text = key.text.empty() ? detail::format_double(key.number) : key.text;
                Json* map = into.find(field.name);
                if (!map || !map->is_object()) map = &into.set(field.name, Json::object());
                map->set(key_text, has_value ? std::move(value) : default_value(field));
                continue;
            }
            if (field.list) {
                Json* list = into.find(field.name);
                if (!list || !list->is_array()) list = &into.set(field.name, Json::array());
                if (type_bits == wire::Len && packable(field.kind)) {
                    std::size_t start, stop;
                    if (!r.length(start, stop)) return fail(error, r.error);
                    wire::Reader packed(*r.data, start, stop);
                    while (!packed.done()) {
                        Json element;
                        if (!read_value(packed, wire_type(field.kind), field.kind, field.type, depth + 1, element, error)) return false;
                        list->items.push_back(std::move(element));
                    }
                } else {
                    Json element;
                    if (!read_value(r, type_bits, field.kind, field.type, depth + 1, element, error)) return false;
                    list->items.push_back(std::move(element));
                }
                continue;
            }
            if (field.oneof != 0)
                for (const Field& other : m->fields)
                    if (other.oneof == field.oneof && other.no != field.no) into.erase(other.name);
            Json* existing = into.find(field.name);
            if (field.kind == "message" && type_bits == wire::Len && existing && existing->is_object() && !is_special(field.type)) {
                std::size_t start, stop;
                if (!r.length(start, stop)) return fail(error, r.error);
                wire::Reader nested(*r.data, start, stop);
                if (!read_message(field.type, nested, depth + 1, *existing, error)) return false;
                continue;
            }
            Json value;
            if (!read_value(r, type_bits, field.kind, field.type, depth + 1, value, error)) return false;
            into.set(field.name, std::move(value));
        }
        for (const Field& field : m->fields) {
            const Json* value = into.find(field.name);
            if (value && !field.list && field.map_key.empty() && !field.presence && field.kind != "message" && is_default(field, *value)) into.erase(field.name);
        }
        return true;
    }

    Json default_value(const Field& field) const {
        if (field.kind == "message") { Json out; std::string ignored; to_special_json(field.type, Json::object(), out, ignored); return out; }
        if (field.kind == "string" || field.kind == "bytes") return Json::from_string("");
        if (field.kind == "bool") return Json::from_bool(false);
        if (field.kind == "enum") {
            auto e = enum_names_.find(field.type);
            if (e != enum_names_.end()) { auto n = e->second.find(0); if (n != e->second.end()) return Json::from_string(n->second); }
            return Json::from_int(0);
        }
        return long_kind(field.kind) ? Json::from_string("0") : Json::from_int(0);
    }

    static Json real(double v) {
        if (std::isnan(v)) return Json::from_string("NaN");
        if (std::isinf(v)) return Json::from_string(v > 0 ? "Infinity" : "-Infinity");
        return Json::from_double(v);
    }

    bool read_value(wire::Reader& r, int type_bits, const std::string& kind, const std::string& type, int depth, Json& out, std::string& error) const {
        if (type_bits != wire_type(kind)) return fail(error, "wrong wire type for " + kind);
        std::uint64_t v = 0;
        std::uint32_t v32 = 0;
        if (kind == "message") {
            std::size_t start, stop;
            if (!r.length(start, stop)) return fail(error, r.error);
            wire::Reader nested(*r.data, start, stop);
            Json message = Json::object();
            if (!read_message(type, nested, depth, message, error)) return false;
            return to_special_json(type, message, out, error);
        }
        if (kind == "string" || kind == "bytes") {
            std::size_t start, stop;
            if (!r.length(start, stop)) return fail(error, r.error);
            if (kind == "bytes") { out = Json::from_string(base64_encode(r.data->substr(start, stop - start))); return true; }
            if (!wire::valid_utf8(*r.data, start, stop)) return fail(error, "invalid UTF-8");
            out = Json::from_string(r.data->substr(start, stop - start));
            return true;
        }
        if (type_bits == wire::Varint) { if (!r.varint(v)) return fail(error, r.error); }
        else if (type_bits == wire::I32) { if (!r.fixed32(v32)) return fail(error, r.error); }
        else if (!r.fixed64(v)) return fail(error, r.error);
        if (kind == "bool") { out = Json::from_bool(v != 0); return true; }
        if (kind == "enum") {
            const std::int32_t n = static_cast<std::int32_t>(v);
            auto e = enum_names_.find(type);
            if (e != enum_names_.end()) { auto name = e->second.find(n); if (name != e->second.end()) { out = Json::from_string(name->second); return true; } }
            out = Json::from_int(n);
            return true;
        }
        if (kind == "int32") { out = Json::from_int(static_cast<std::int32_t>(v)); return true; }
        if (kind == "uint32") { out = Json::from_uint(static_cast<std::uint32_t>(v)); return true; }
        if (kind == "sint32") { const std::uint32_t n = static_cast<std::uint32_t>(v); out = Json::from_int(static_cast<std::int32_t>((n >> 1) ^ (~(n & 1) + 1))); return true; }
        if (kind == "int64") { out = Json::from_string(std::to_string(static_cast<std::int64_t>(v))); return true; }
        if (kind == "uint64") { out = Json::from_string(std::to_string(v)); return true; }
        if (kind == "sint64") { out = Json::from_string(std::to_string(static_cast<std::int64_t>((v >> 1) ^ (~(v & 1) + 1)))); return true; }
        if (kind == "fixed32") { out = Json::from_uint(v32); return true; }
        if (kind == "sfixed32") { out = Json::from_int(static_cast<std::int32_t>(v32)); return true; }
        if (kind == "fixed64") { out = Json::from_string(std::to_string(v)); return true; }
        if (kind == "sfixed64") { out = Json::from_string(std::to_string(static_cast<std::int64_t>(v))); return true; }
        if (kind == "float") { float f; std::memcpy(&f, &v32, 4); out = real(static_cast<double>(f)); return true; }
        if (kind == "double") { double d; std::memcpy(&d, &v, 8); out = real(d); return true; }
        return fail(error, "unsupported kind " + kind);
    }

    bool to_special_json(const std::string& type, const Json& value, Json& out, std::string& error) const {
        if (type.compare(0, 16, "google.protobuf.") != 0) { out = value; return true; }
        std::int64_t seconds = 0;
        std::int32_t nanos = 0;
        if (const Json* s = value.find("seconds")) seconds = std::strtoll(s->text.c_str(), nullptr, 10);
        if (const Json* n = value.find("nanos")) nanos = static_cast<std::int32_t>(n->number);
        if (type == "google.protobuf.Timestamp") {
            std::string text;
            if (!format_timestamp(seconds, nanos, text)) return fail(error, "timestamp out of range");
            out = Json::from_string(text);
            return true;
        }
        if (type == "google.protobuf.Duration") {
            std::string text;
            if (!format_duration(seconds, nanos, text)) return fail(error, "duration out of range");
            out = Json::from_string(text);
            return true;
        }
        if (const char* kind = wrapper_kind(type)) {
            if (const Json* inner = value.find("value")) { out = *inner; return true; }
            Field f; f.kind = kind; f.name = "value";
            out = default_value(f);
            return true;
        }
        out = value;
        return true;
    }

    // ---- Timestamp / Duration in Go protojson formats ----

    static std::int64_t floor_div(std::int64_t a, std::int64_t b) { return a >= 0 ? a / b : -((-a + b - 1) / b); }
    static std::int64_t civil_days(std::int64_t year, std::int64_t month, std::int64_t day) {
        const std::int64_t y = year + 400 - (month <= 2 ? 1 : 0);
        const std::int64_t era = floor_div(y, 400);
        const std::int64_t yoe = y - era * 400;
        const std::int64_t doy = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
        return era * 146097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719468 - 146097;
    }
    static std::string fraction(std::int32_t nanos) {
        if (nanos == 0) return "";
        char digits[16];
        std::snprintf(digits, sizeof digits, "%09d", nanos);
        std::string d(digits);
        if (nanos % 1000000 == 0) return "." + d.substr(0, 3);
        if (nanos % 1000 == 0) return "." + d.substr(0, 6);
        return "." + d;
    }
    static bool digits(const std::string& s, std::size_t& pos, std::size_t min, std::size_t max, std::int64_t& out) {
        std::size_t n = 0;
        out = 0;
        while (pos < s.size() && n < max && s[pos] >= '0' && s[pos] <= '9') { out = out * 10 + (s[pos] - '0'); ++pos; ++n; }
        return n >= min;
    }
    static bool nanos_from(const std::string& s, std::size_t& pos, std::size_t max, std::int32_t& nanos) {
        std::string frac;
        while (pos < s.size() && s[pos] >= '0' && s[pos] <= '9') frac += s[pos++];
        if (frac.empty() || frac.size() > max) return false;
        frac = (frac + "000000000").substr(0, 9);
        nanos = static_cast<std::int32_t>(std::strtol(frac.c_str(), nullptr, 10));
        return true;
    }
    static bool parse_timestamp(const std::string& s, std::int64_t& seconds, std::int32_t& nanos) {
        std::size_t p = 0;
        std::int64_t year, month, day, hour, minute, second;
        auto take = [&](char c) { if (p < s.size() && s[p] == c) { ++p; return true; } return false; };
        if (!digits(s, p, 4, 4, year) || !take('-') || !digits(s, p, 2, 2, month) || !take('-') || !digits(s, p, 2, 2, day) || !take('T') ||
            !digits(s, p, 1, 2, hour) || !take(':') || !digits(s, p, 2, 2, minute) || !take(':') || !digits(s, p, 2, 2, second)) return false;
        nanos = 0;
        if (p < s.size() && (s[p] == '.' || s[p] == ',')) { ++p; if (!nanos_from(s, p, 1000, nanos)) return false; }
        seconds = civil_days(year, month, day) * 86400 + hour * 3600 + minute * 60 + second;
        if (take('Z')) return p == s.size();
        if (p >= s.size() || (s[p] != '+' && s[p] != '-')) return false;
        const bool negative = s[p++] == '-';
        std::int64_t oh, om;
        if (!digits(s, p, 2, 2, oh) || !take(':') || !digits(s, p, 2, 2, om) || p != s.size()) return false;
        seconds -= (negative ? -1 : 1) * (oh * 3600 + om * 60);
        return true;
    }
    static bool format_timestamp(std::int64_t seconds, std::int32_t nanos, std::string& out) {
        if (seconds < -62135596800LL || seconds > 253402300799LL || nanos < 0 || nanos > 999999999) return false;
        const std::int64_t days = floor_div(seconds, 86400);
        const std::int64_t rest = seconds - days * 86400;
        // Howard Hinnant's civil_from_days.
        const std::int64_t z = days + 719468;
        const std::int64_t era = floor_div(z, 146097);
        const std::int64_t doe = z - era * 146097;
        const std::int64_t yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
        const std::int64_t doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
        const std::int64_t mp = (5 * doy + 2) / 153;
        const std::int64_t d = doy - (153 * mp + 2) / 5 + 1;
        const std::int64_t m = mp < 10 ? mp + 3 : mp - 9;
        const std::int64_t y = yoe + era * 400 + (m <= 2 ? 1 : 0);
        char buf[128];
        std::snprintf(buf, sizeof buf, "%04lld-%02lld-%02lldT%02lld:%02lld:%02lld", static_cast<long long>(y), static_cast<long long>(m), static_cast<long long>(d),
                      static_cast<long long>(rest / 3600), static_cast<long long>(rest % 3600 / 60), static_cast<long long>(rest % 60));
        out = std::string(buf) + fraction(nanos) + "Z";
        return true;
    }
    static bool parse_duration(const std::string& s, std::int64_t& seconds, std::int32_t& nanos) {
        std::size_t p = 0;
        const bool negative = p < s.size() && s[p] == '-';
        if (negative) ++p;
        std::int64_t whole;
        if (!digits(s, p, 1, 18, whole)) return false;
        nanos = 0;
        if (p < s.size() && s[p] == '.') { ++p; if (!nanos_from(s, p, 9, nanos)) return false; }
        if (p + 1 != s.size() || s[p] != 's') return false;
        seconds = negative ? -whole : whole;
        if (negative) nanos = -nanos;
        return true;
    }
    static bool format_duration(std::int64_t seconds, std::int32_t nanos, std::string& out) {
        if (seconds < -315576000000LL || seconds > 315576000000LL || nanos < -999999999 || nanos > 999999999 || (seconds > 0 && nanos < 0) || (seconds < 0 && nanos > 0)) return false;
        const bool negative = seconds < 0 || nanos < 0;
        out = (negative ? "-" : "") + std::to_string(seconds < 0 ? -seconds : seconds) + fraction(nanos < 0 ? -nanos : nanos) + "s";
        return true;
    }
};

// ---------------------------------------------------------------- frames

constexpr const char* subprotocol_proto = "tabforge.realtime.v1.proto";

enum Kind : int { Hello = 1, Welcome = 2, Request = 3, Response = 4, Push = 5, Notify = 6, Ping = 7, Pong = 8, AckFrame = 9, Close = 10 };

inline const char* kind_name(int kind) {
    static const char* names[] = {"kind(0)", "hello", "welcome", "request", "response", "push", "notify", "ping", "pong", "ack", "close"};
    return kind > 0 && kind <= 10 ? names[kind] : "unknown kind";
}

struct Error {
    std::string code, message;
    bool retryable = false;
};

struct Frame {
    int kind = 0;
    std::uint64_t seq = 0, ack = 0, reply_to = 0;
    std::uint32_t message = 0;
    bool has_payload = false;
    std::string payload;
    bool has_error = false;
    Error error;
    bool has_hello = false;
    std::string hello_version, hello_schema_hash, hello_token, hello_session_id, hello_resume_token;
    bool has_welcome = false, welcome_resumed = false;
    std::string welcome_session_id, welcome_resume_token;
    std::uint32_t welcome_heartbeat_ms = 0, welcome_resume_window_ms = 0;

    std::string encode() const {
        wire::Writer w;
        auto number = [&](int no, std::uint64_t v) { if (v) { w.tag(no, wire::Varint); w.varint(v); } };
        auto text = [](wire::Writer& target, int no, const std::string& s) { if (!s.empty()) { target.tag(no, wire::Len); target.bytes(s); } };
        number(1, static_cast<std::uint64_t>(kind));
        number(2, seq);
        number(3, ack);
        number(4, message);
        number(5, reply_to);
        if (!payload.empty()) { w.tag(6, wire::Len); w.bytes(payload); }
        if (has_error) {
            wire::Writer e;
            text(e, 1, error.code);
            text(e, 2, error.message);
            if (error.retryable) { e.tag(3, wire::Varint); e.varint(1); }
            w.tag(7, wire::Len);
            w.bytes(e.out);
        }
        if (kind == Hello) {
            wire::Writer h;
            text(h, 1, hello_version);
            text(h, 2, hello_schema_hash);
            text(h, 3, hello_token);
            text(h, 4, hello_session_id);
            text(h, 5, hello_resume_token);
            w.tag(8, wire::Len);
            w.bytes(h.out);
        }
        if (has_welcome) { // server side; used by tests that play the server
            wire::Writer v;
            text(v, 1, welcome_session_id);
            text(v, 2, welcome_resume_token);
            if (welcome_resumed) { v.tag(3, wire::Varint); v.varint(1); }
            if (welcome_heartbeat_ms) { v.tag(4, wire::Varint); v.varint(welcome_heartbeat_ms); }
            if (welcome_resume_window_ms) { v.tag(5, wire::Varint); v.varint(welcome_resume_window_ms); }
            w.tag(9, wire::Len);
            w.bytes(v.out);
        }
        return w.out;
    }

    // Decodes a binary frame; unknown fields are skipped.
    static bool decode(const std::string& data, Frame& f, std::string& why) {
        f = Frame();
        wire::Reader r(data, 0, data.size());
        while (!r.done()) {
            std::uint64_t tag;
            if (!r.varint(tag)) { why = r.error; return false; }
            const std::uint64_t no = tag >> 3;
            const int type = static_cast<int>(tag & 7);
            if (no == 0) { why = "field number"; return false; }
            if ((no <= 5 && type != wire::Varint) || (no >= 6 && no <= 9 && type != wire::Len)) {
                if (no <= 9) { why = "wire type of field " + std::to_string(no); return false; }
            }
            if (no > 9) {
                if (!r.skip(type, 0)) { why = r.error; return false; }
                continue;
            }
            std::uint64_t v = 0;
            std::size_t start = 0, stop = 0;
            if (type == wire::Varint) { if (!r.varint(v)) { why = r.error; return false; } }
            else if (!r.length(start, stop)) { why = r.error; return false; }
            switch (no) {
            case 1: f.kind = v > 100 ? -1 : static_cast<int>(v); break;
            case 2: f.seq = v; break;
            case 3: f.ack = v; break;
            case 4: if (v > 65535) { why = "message id"; return false; } f.message = static_cast<std::uint32_t>(v); break;
            case 5: f.reply_to = v; break;
            case 6: f.has_payload = true; f.payload = data.substr(start, stop - start); break;
            case 7: f.has_error = true; if (!sub(data, start, stop, why, [&](std::uint64_t n, int t, std::uint64_t x, std::size_t a, std::size_t b) {
                        if (((n == 1 || n == 2) && t != wire::Len) || (n == 3 && t != wire::Varint)) return false;
                        if (n == 1) f.error.code = data.substr(a, b - a);
                        else if (n == 2) f.error.message = data.substr(a, b - a);
                        else if (n == 3) f.error.retryable = x != 0;
                        return true; })) return false;
                break;
            case 8: f.has_hello = true; if (!sub(data, start, stop, why, [&](std::uint64_t n, int t, std::uint64_t, std::size_t a, std::size_t b) {
                        if (n >= 1 && n <= 5 && t != wire::Len) return false;
                        std::string* fields[] = {&f.hello_version, &f.hello_schema_hash, &f.hello_token, &f.hello_session_id, &f.hello_resume_token};
                        if (n >= 1 && n <= 5) *fields[n - 1] = data.substr(a, b - a);
                        return true; })) return false;
                break;
            case 9: f.has_welcome = true; if (!sub(data, start, stop, why, [&](std::uint64_t n, int t, std::uint64_t x, std::size_t a, std::size_t b) {
                        if (((n == 1 || n == 2) && t != wire::Len) || (n >= 3 && n <= 5 && t != wire::Varint)) return false;
                        if (n == 1) f.welcome_session_id = data.substr(a, b - a);
                        else if (n == 2) f.welcome_resume_token = data.substr(a, b - a);
                        else if (n == 3) f.welcome_resumed = x != 0;
                        else if (n == 4) { if (x > 0xffffffffULL) return false; f.welcome_heartbeat_ms = static_cast<std::uint32_t>(x); }
                        else if (n == 5) f.welcome_resume_window_ms = x > 0xffffffffULL ? 0 : static_cast<std::uint32_t>(x);
                        return true; })) return false;
                break;
            }
        }
        return true;
    }

    // Server-to-client envelope rules shared with Go, TypeScript, C# and GDScript.
    bool validate_from_server(std::string& why) const {
        const bool control = seq == 0 && message == 0 && reply_to == 0 && !has_payload && !has_hello;
        auto bad = [&](const char* what) { why = std::string("malformed frame: ") + what; return false; };
        switch (kind) {
        case Welcome:
            if (!control || !has_welcome || welcome_session_id.empty() || welcome_resume_token.empty() || welcome_heartbeat_ms == 0 || has_error) return bad("welcome");
            return true;
        case Response:
            if (seq == 0 || message == 0 || reply_to == 0 || has_hello || has_welcome || (has_error && has_payload)) return bad("response");
            if (has_error && error.code.empty()) return bad("error code");
            return true;
        case Push:
            if (message == 0 || reply_to != 0 || has_error || has_hello || has_welcome) return bad("push");
            return true;
        case Pong:
        case AckFrame:
            if (!control || has_error || has_welcome) return bad(kind_name(kind));
            return true;
        case Close:
            if (!control || !has_error || error.code.empty() || has_welcome) return bad("close");
            return true;
        default:
            why = std::string("malformed frame: unexpected ") + kind_name(kind) + " from server";
            return false;
        }
    }

private:
    template <typename Visit>
    static bool sub(const std::string& data, std::size_t start, std::size_t stop, std::string& why, Visit visit) {
        wire::Reader r(data, start, stop);
        while (!r.done()) {
            std::uint64_t tag, v = 0;
            if (!r.varint(tag)) { why = r.error; return false; }
            const int type = static_cast<int>(tag & 7);
            std::size_t a = 0, b = 0;
            if (type == wire::Varint) { if (!r.varint(v)) { why = r.error; return false; } }
            else if (type == wire::Len) {
                if (!r.length(a, b)) { why = r.error; return false; }
                if (!wire::valid_utf8(data, a, b)) { why = "invalid UTF-8"; return false; }
            } else if (!r.skip(type, 0)) { why = r.error; return false; }
            if ((tag >> 3) == 0 || !visit(tag >> 3, type, v, a, b)) { why = "malformed nested message"; return false; }
        }
        return true;
    }
};

// ---------------------------------------------------------------- session engine

enum class State { Idle, Connecting, Connected, Resumed, Disconnected, Closed };

inline const char* state_name(State s) {
    switch (s) {
    case State::Idle: return "idle";
    case State::Connecting: return "connecting";
    case State::Connected: return "connected";
    case State::Resumed: return "resumed";
    case State::Disconnected: return "disconnected";
    case State::Closed: return "closed";
    }
    return "?";
}

struct Spec {
    std::string name, kind, type, response_type;
    std::uint32_t id = 0;
    std::int64_t timeout_ms = 0;
    bool is_volatile = false;
};

// The platform WebSocket. Methods are called from the thread that calls
// Client::tick; events go back through Client::opened/received/closed, which
// may be called from any thread (they are queued until the next tick).
class Socket {
public:
    virtual ~Socket() = default;
    // Starts connecting with one offered subprotocol. Returns an id > 0, or 0
    // if the attempt could not start (the client retries after a backoff).
    virtual std::uint64_t open(const std::string& subprotocol) = 0;
    // Sends one binary WebSocket message; false if the socket is not usable.
    virtual bool send(std::uint64_t id, const std::string& frame) = 0;
    // Closes after what is already queued. Must not call closed() synchronously.
    virtual void close(std::uint64_t id) = 0;
};

struct ClientOptions {
    std::int64_t min_backoff_ms = 200;
    std::int64_t max_backoff_ms = 10000;
    std::size_t max_unacked = 1024;
    std::uint64_t seed = 0; // 0 picks a random seed for the backoff jitter
};

struct Reply {
    bool ok = false;
    std::string payload; // binary Protobuf of the response type when ok
    Error error;
};

// The session engine shared by the Unreal client and the native tests. Not
// thread-safe except for opened/received/closed; call everything else and
// tick() from one thread. User callbacks run inside tick() (or close()).
class Client {
public:
    using Clock = std::function<std::int64_t()>;
    std::function<void(State, const Error*)> on_state;
    // Receives every push of a known id; volatile pushes have seq 0.
    std::function<void(const Spec&, const std::string& payload)> on_push;
    // Called for the bearer token of a new session (not for resumes).
    std::function<std::string()> token;

    Client(Socket& socket, Clock clock = nullptr) : socket_(socket), clock_(clock ? std::move(clock) : Clock(steady_ms)) {}

    // Loads the channel from runtime.json (parsed). Call once before connect.
    bool configure(const Json& runtime, const std::string& channel, const ClientOptions& options, std::string& error) {
        options_ = options;
        rng_.seed(options.seed ? options.seed : std::random_device{}());
        const Json* channels = runtime.find("channels");
        const Json* ch = channels ? channels->find(channel) : nullptr;
        if (!runtime.find("version") || !runtime.find("version")->is_string() || !runtime.find("schemaHash") || !ch || !ch->is_object()) {
            error = "Load the generated runtime.json of a contract with channel " + channel;
            return false;
        }
        version_ = runtime.string_or("version");
        schema_hash_ = runtime.string_or("schemaHash");
        heartbeat_ms_ = static_cast<std::int64_t>(ch->number_or("heartbeatMS", 5000));
        const Json* messages = ch->find("messages");
        if (!messages || !messages->is_object()) { error = "channel " + channel + " has no messages"; return false; }
        for (const auto& m : messages->members) {
            Spec s;
            s.name = m.first;
            s.kind = m.second.string_or("kind");
            s.type = m.second.string_or("type");
            s.response_type = m.second.string_or("responseType");
            s.id = static_cast<std::uint32_t>(m.second.number_or("id"));
            s.timeout_ms = static_cast<std::int64_t>(m.second.number_or("timeoutMS"));
            s.is_volatile = m.second.bool_or("volatile");
            by_id_[s.id] = s.name;
            by_name_[s.name] = s;
        }
        return true;
    }

    const Spec* spec(const std::string& name) const { auto it = by_name_.find(name); return it == by_name_.end() ? nullptr : &it->second; }
    State state() const { return state_; }
    const std::string& session_id() const { return session_id_; }
    std::size_t unacked() const { return outbox_.size(); }

    // Starts connecting; progress is reported through on_state.
    bool connect(Error& error) {
        if (closed_) { error = closed_error_; return false; }
        if (state_ == State::Idle) open();
        flush();
        return true;
    }

    // Sends a request (payload: binary Protobuf of the request type). done runs
    // exactly once, inside a later tick(). timeout_ms 0 uses the contract value;
    // a smaller value shortens it.
    bool request(const std::string& name, std::string payload, std::int64_t timeout_ms, std::function<void(const Reply&)> done, Error& error) {
        const Spec* s = spec(name);
        if (!s || s->kind != "request") { error = {"invalid_message", "No request message " + name, false}; return false; }
        if (closed_) { error = closed_error_; return false; }
        if (outbox_.size() >= options_.max_unacked) { error = {"overloaded", "Too many unacknowledged messages", true}; return false; }
        auto call = std::make_shared<Call>();
        call->done = std::move(done);
        const std::int64_t timeout = timeout_ms > 0 ? std::min(timeout_ms, s->timeout_ms) : s->timeout_ms;
        call->deadline = now() + timeout;
        Out f;
        f.kind = Request;
        f.message = s->id;
        f.payload = std::move(payload);
        f.call = call;
        enqueue(std::move(f));
        flush();
        return true;
    }

    // Reliable notifies queue while offline. Volatile ones are dropped (false)
    // when there is no ready connection.
    bool notify(const std::string& name, std::string payload, Error& error) {
        const Spec* s = spec(name);
        if (!s || s->kind != "notify") { error = {"invalid_message", "No notify message " + name, false}; return false; }
        if (closed_) { error = closed_error_; return false; }
        Out f;
        f.kind = Notify;
        f.message = s->id;
        f.payload = std::move(payload);
        if (!s->is_volatile) {
            if (outbox_.size() >= options_.max_unacked) { error = {"overloaded", "Too many unacknowledged messages", true}; return false; }
            enqueue(std::move(f));
            flush();
            return true;
        }
        if (!conn_ || !conn_->ready) { error = {"dropped", "Volatile message dropped while offline", true}; return false; }
        write(*conn_, f);
        flush();
        return true;
    }

    // Ends the session on the server and stops reconnecting.
    void close() {
        shutdown({"closed", "Closed by client", false}, true);
        flush();
    }

    // Socket events, from any thread.
    void opened(std::uint64_t id, const std::string& protocol) { post(Event{Event::Opened, id, protocol, false}); }
    void received(std::uint64_t id, std::string data, bool text) { post(Event{Event::Message, id, std::move(data), text}); }
    // reason is the WebSocket close reason ("" if none or not a normal close).
    void closed(std::uint64_t id, const std::string& reason) { post(Event{Event::Closed, id, reason, false}); }

    // Processes socket events, timers and callbacks.
    void tick() {
        std::deque<Event> events;
        {
            std::lock_guard<std::mutex> lock(inbox_mutex_);
            events.swap(inbox_);
        }
        for (Event& e : events) {
            if (!conn_ || conn_->id != e.id) continue; // a socket this client already dropped
            switch (e.type) {
            case Event::Opened: on_opened(*conn_, e.data); break;
            case Event::Message: on_received(*conn_, e.data, e.text); break;
            case Event::Closed: on_socket_closed(*conn_, e.data); break;
            }
        }
        timers(now());
        flush();
    }

private:
    struct Call {
        std::function<void(const Reply&)> done;
        std::int64_t deadline = 0;
        bool finished = false;
    };
    struct Out {
        int kind = 0;
        std::uint64_t seq = 0;
        std::uint32_t message = 0;
        std::string payload;
        bool written = false;
        std::shared_ptr<Call> call;
    };
    struct Connection {
        std::uint64_t id = 0;
        bool opened = false, ready = false;
        std::int64_t idle_deadline = 0, next_ping = 0;
    };
    struct Event {
        enum Type { Opened, Message, Closed } type;
        std::uint64_t id;
        std::string data;
        bool text;
    };

    Socket& socket_;
    Clock clock_;
    ClientOptions options_;
    std::mt19937_64 rng_;
    std::string version_, schema_hash_;
    std::unordered_map<std::string, Spec> by_name_;
    std::unordered_map<std::uint32_t, std::string> by_id_;
    std::mutex inbox_mutex_;
    std::deque<Event> inbox_;
    std::vector<std::function<void()>> callbacks_;

    State state_ = State::Idle;
    std::string session_id_, resume_token_;
    std::int64_t heartbeat_ms_ = 5000;
    std::uint64_t next_seq_ = 0, peer_ack_ = 0, received_ = 0, ack_sent_ = 0;
    std::deque<Out> outbox_;
    std::unordered_map<std::uint64_t, std::shared_ptr<Call>> pending_;
    std::unique_ptr<Connection> conn_;
    int failures_ = 0;
    std::int64_t retry_at_ = -1, ack_at_ = -1;
    bool closed_ = false;
    Error closed_error_;

    static std::int64_t steady_ms() {
        return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now().time_since_epoch()).count();
    }
    std::int64_t now() const { return clock_(); }

    void post(Event e) {
        std::lock_guard<std::mutex> lock(inbox_mutex_);
        inbox_.push_back(std::move(e));
    }

    // Runs queued user callbacks; callbacks may call back into the client.
    void flush() {
        while (!callbacks_.empty()) {
            std::vector<std::function<void()>> run;
            run.swap(callbacks_);
            for (auto& f : run) f();
        }
    }

    void set_state(State next, const Error* error = nullptr) {
        state_ = next;
        if (!on_state) return;
        auto handler = on_state;
        if (error) {
            Error copy = *error;
            callbacks_.push_back([handler, next, copy] { handler(next, &copy); });
        } else {
            callbacks_.push_back([handler, next] { handler(next, nullptr); });
        }
    }

    void finish(const std::shared_ptr<Call>& call, Reply response) {
        if (call->finished) return;
        call->finished = true;
        callbacks_.push_back([call, response] { if (call->done) call->done(response); });
    }
    static Reply failure(const Error& e) { Reply r; r.error = e; return r; }

    void enqueue(Out f) {
        f.seq = ++next_seq_;
        if (f.call) pending_[f.seq] = f.call;
        outbox_.push_back(std::move(f));
        if (conn_ && conn_->ready) write(*conn_, outbox_.back());
    }

    void write(Connection& c, Out& f) {
        if (f.seq != 0 && f.seq <= peer_ack_) return;
        f.written = true;
        ack_sent_ = received_;
        Frame frame;
        frame.kind = f.kind;
        frame.seq = f.seq;
        frame.ack = received_;
        frame.message = f.message;
        frame.payload = f.payload;
        send(c, frame);
    }

    void send(Connection& c, const Frame& frame) {
        if (!socket_.send(c.id, frame.encode())) drop(c, nullptr);
    }

    void open() {
        if (closed_) return;
        retry_at_ = -1;
        set_state(State::Connecting);
        auto c = std::make_unique<Connection>();
        c->idle_deadline = now() + heartbeat_ms_ * 2 + 1000;
        c->id = socket_.open(subprotocol_proto);
        if (c->id == 0) {
            schedule_retry(false);
            return;
        }
        conn_ = std::move(c);
    }

    void on_opened(Connection& c, const std::string& protocol) {
        if (!protocol.empty() && protocol != subprotocol_proto) {
            shutdown({"protocol_error", std::string("Server did not accept ") + subprotocol_proto, false}, false);
            return;
        }
        c.opened = true;
        Frame hello;
        hello.kind = Hello;
        hello.ack = received_;
        hello.hello_version = version_;
        hello.hello_schema_hash = schema_hash_;
        if (session_id_.empty() && token) hello.hello_token = token();
        hello.hello_session_id = session_id_;
        hello.hello_resume_token = resume_token_;
        send(c, hello);
    }

    // Forgets the connection and schedules a reconnect (or stops for good).
    void drop(Connection& c, const Error* error) {
        if (!conn_ || conn_.get() != &c) return;
        const bool was_ready = c.ready;
        const std::uint64_t id = c.id;
        conn_.reset();
        socket_.close(id);
        if (closed_) return;
        if (error && !error->retryable) {
            shutdown(*error, false);
            return;
        }
        if (was_ready || state_ != State::Disconnected) set_state(State::Disconnected, error);
        schedule_retry(false);
    }

    void schedule_retry(bool immediate) {
        if (closed_) return;
        std::int64_t delay = 0;
        if (!immediate) {
            const double bound = std::min(static_cast<double>(options_.max_backoff_ms), static_cast<double>(options_.min_backoff_ms) * std::pow(2.0, std::min(failures_, 16)));
            std::uniform_real_distribution<double> jitter(0.0, 1.0);
            delay = static_cast<std::int64_t>(bound / 2 + jitter(rng_) * bound / 2);
            ++failures_;
        }
        if (state_ != State::Disconnected) set_state(State::Disconnected);
        retry_at_ = now() + delay;
    }

    void timers(std::int64_t t) {
        if (closed_) return;
        if (retry_at_ >= 0 && t >= retry_at_ && !conn_) open();
        if (conn_ && t >= conn_->idle_deadline) drop(*conn_, nullptr);
        if (conn_ && conn_->ready && t >= conn_->next_ping) {
            conn_->next_ping = t + heartbeat_ms_;
            Frame ping;
            ping.kind = Ping;
            ping.ack = received_;
            send(*conn_, ping);
        }
        if (ack_at_ >= 0 && t >= ack_at_) {
            ack_at_ = -1;
            if (conn_ && conn_->ready && received_ > ack_sent_) {
                ack_sent_ = received_;
                Frame ack;
                ack.kind = AckFrame;
                ack.ack = received_;
                send(*conn_, ack);
            }
        }
        std::vector<std::uint64_t> expired;
        for (const auto& p : pending_) if (t >= p.second->deadline) expired.push_back(p.first);
        std::sort(expired.begin(), expired.end());
        for (std::uint64_t seq : expired) {
            auto call = pending_[seq];
            pending_.erase(seq);
            finish(call, failure({"timeout", "Request deadline exceeded", false}));
        }
    }

    static bool close_code(const std::string& reason) {
        if (reason.empty() || reason.size() > 64 || reason[0] < 'a' || reason[0] > 'z') return false;
        for (char ch : reason) if (!((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_')) return false;
        return true;
    }

    // The server writes its error code as the WebSocket close reason, so a
    // close frame lost with the connection still stops the client correctly.
    void on_socket_closed(Connection& c, const std::string& reason) {
        if (c.opened && close_code(reason)) {
            const bool retryable = reason == "session_expired" || reason == "server_shutdown" || reason == "overloaded";
            server_closed(c, {reason, "Closed by server", retryable});
            return;
        }
        drop(c, nullptr);
    }

    void on_received(Connection& c, const std::string& data, bool text) {
        if (data.empty()) return; // Empty messages are not frames.
        Frame f;
        std::string why;
        if (text) why = "text frame on binary subprotocol";
        else if (!Frame::decode(data, f, why)) why = "malformed frame: " + why;
        else f.validate_from_server(why);
        if (!why.empty()) {
            const Error e{"protocol_error", why, true};
            drop(c, &e);
            return;
        }
        c.idle_deadline = now() + heartbeat_ms_ * 2 + 1000;
        if (f.kind == Close) {
            server_closed(c, f.error);
            return;
        }
        if (!c.ready) {
            if (f.kind != Welcome) { const Error e{"protocol_error", "Expected welcome", true}; drop(c, &e); }
            else welcome(c, f);
            return;
        }
        if (f.kind == Welcome) { const Error e{"protocol_error", "Unexpected welcome", true}; drop(c, &e); return; }
        if (!apply_ack(c, f.ack) || f.kind == Pong || f.kind == AckFrame) return;
        if (f.kind == Push && f.seq == 0) { deliver_push(f); return; }
        if (f.seq <= received_) {
            ack_sent_ = 0; // A retransmission: acknowledge again.
            schedule_ack();
            return;
        }
        if (f.seq != received_ + 1) { const Error e{"protocol_error", "Sequence gap from server", true}; drop(c, &e); return; }
        received_ = f.seq;
        schedule_ack();
        if (f.kind == Push) { deliver_push(f); return; }
        auto it = pending_.find(f.reply_to);
        if (it == pending_.end()) return; // Timed out; the late answer is discarded.
        auto call = it->second;
        pending_.erase(it);
        if (f.has_error) { finish(call, failure(f.error)); return; }
        Reply ok;
        ok.ok = true;
        ok.payload = f.payload;
        finish(call, ok);
    }

    void welcome(Connection& c, const Frame& f) {
        const bool resuming = !session_id_.empty();
        if (f.welcome_resumed && f.welcome_session_id != session_id_) {
            const Error e{"protocol_error", "Server resumed a different session", false};
            drop(c, &e);
            return;
        }
        if (!f.welcome_resumed) {
            if (resuming) reset_session();
            received_ = 0;
            peer_ack_ = 0;
        }
        session_id_ = f.welcome_session_id;
        resume_token_ = f.welcome_resume_token;
        heartbeat_ms_ = f.welcome_heartbeat_ms;
        c.ready = true;
        failures_ = 0;
        if (!apply_ack(c, f.ack)) return;
        ack_sent_ = 0;
        const std::uint64_t id = c.id;
        for (std::size_t i = 0; i < outbox_.size() && conn_ && conn_->id == id; ++i) write(c, outbox_[i]);
        if (!conn_ || conn_->id != id) return;
        c.next_ping = now() + heartbeat_ms_;
        c.idle_deadline = now() + heartbeat_ms_ * 2 + 1000;
        set_state(f.welcome_resumed ? State::Resumed : State::Connected);
    }

    bool apply_ack(Connection& c, std::uint64_t ack) {
        if (ack > next_seq_) {
            const Error e{"protocol_error", "Server acknowledged an unsent frame", true};
            drop(c, &e);
            return false;
        }
        if (ack > peer_ack_) {
            peer_ack_ = ack;
            while (!outbox_.empty() && outbox_.front().seq <= ack) outbox_.pop_front();
        }
        return true;
    }

    void schedule_ack() { if (ack_at_ < 0) ack_at_ = now() + 50; }

    void deliver_push(const Frame& f) {
        auto name = by_id_.find(f.message);
        if (name == by_id_.end()) return; // Pushes added by a newer server are ignored.
        const Spec& s = by_name_.at(name->second);
        if (s.kind != "push" || !on_push) return;
        auto handler = on_push;
        const Spec copy = s;
        const std::string payload = f.payload;
        callbacks_.push_back([handler, copy, payload] { handler(copy, payload); });
    }

    void server_closed(Connection& c, const Error& error) {
        if (error.code == "session_expired" && !session_id_.empty()) {
            // The server forgot the session: start a new one right away.
            reset_session();
            const std::uint64_t id = c.id;
            conn_.reset();
            socket_.close(id);
            schedule_retry(true);
            return;
        }
        drop(c, &error);
    }

    // Forgets the session. Frames never written move to the next session;
    // requests that may have reached the server fail with session_lost.
    void reset_session() {
        std::deque<Out> kept;
        std::vector<std::shared_ptr<Call>> lost;
        std::unordered_set<Call*> kept_calls;
        for (Out& f : outbox_) {
            if (!f.written) { if (f.call) kept_calls.insert(f.call.get()); kept.push_back(std::move(f)); }
        }
        for (auto& p : pending_) if (!kept_calls.count(p.second.get())) lost.push_back(p.second);
        pending_.clear();
        for (std::size_t i = 0; i < kept.size(); ++i) {
            kept[i].seq = i + 1;
            if (kept[i].call) pending_[kept[i].seq] = kept[i].call;
        }
        outbox_ = std::move(kept);
        next_seq_ = outbox_.size();
        session_id_.clear();
        resume_token_.clear();
        received_ = peer_ack_ = ack_sent_ = 0;
        std::sort(lost.begin(), lost.end(), [](const std::shared_ptr<Call>& a, const std::shared_ptr<Call>& b) { return a->deadline < b->deadline; });
        for (auto& call : lost) finish(call, failure({"session_lost", "Connection lost; the request may or may not have run", false}));
    }

    void shutdown(const Error& error, bool notify_server) {
        if (closed_) return;
        closed_ = true;
        closed_error_ = error;
        retry_at_ = ack_at_ = -1;
        if (conn_) {
            const std::uint64_t id = conn_->id;
            if (notify_server && conn_->ready) {
                Frame f;
                f.kind = Close;
                f.ack = received_;
                f.has_error = true;
                f.error = error;
                socket_.send(id, f.encode());
            }
            conn_.reset();
            socket_.close(id);
        }
        std::vector<std::pair<std::uint64_t, std::shared_ptr<Call>>> calls(pending_.begin(), pending_.end());
        std::sort(calls.begin(), calls.end(), [](const auto& a, const auto& b) { return a.first < b.first; });
        for (auto& p : calls) finish(p.second, failure(error));
        pending_.clear();
        outbox_.clear();
        set_state(State::Closed, &error);
    }
};

} // namespace realtime
} // namespace tabforge
