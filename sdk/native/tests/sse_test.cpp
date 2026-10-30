#include "TabForgeSse.h"
#include "TabForgeJsonSyntax.h"
#include <cassert>
#include <iostream>
#include <vector>

namespace {
// Records the event stream so tests can check raw number capture.
class Recorder final : public tabforge::JsonVisitor {
public:
    std::vector<std::string> events;
    bool begin_object() override { events.push_back("{"); return true; }
    bool key(const std::string& name) override { events.push_back("k:" + name); return true; }
    bool end_object() override { events.push_back("}"); return true; }
    bool begin_array() override { events.push_back("["); return true; }
    bool end_array() override { events.push_back("]"); return true; }
    bool string_value(const std::string& value) override { events.push_back("s:" + value); return true; }
    bool number_value(const std::string& text) override { events.push_back("n:" + text); return true; }
    bool bool_value(bool value) override { events.push_back(value ? "true" : "false"); return true; }
    bool null_value() override { events.push_back("null"); return true; }
};
std::string nested(std::size_t depth) { return std::string(depth, '[') + "0" + std::string(depth, ']'); }
}

int main() {
    const std::string input = "\xef\xbb\xbf: ping\r\n\r\nid: 1\r\nevent: text.delta\r\ndata: {\"text\":\"你好😀\"}\r\n\r\nid: 2\ndata: second\ndata: line\n\n";
    for (std::size_t split = 0; split <= input.size(); ++split) {
        tabforge::SseParser parser; std::vector<tabforge::SseFrame> frames;
        auto receive = [&](const tabforge::SseFrame& frame) { frames.push_back(frame); return true; };
        assert(parser.feed(reinterpret_cast<const std::uint8_t*>(input.data()), split, receive));
        assert(parser.feed(reinterpret_cast<const std::uint8_t*>(input.data() + split), input.size() - split, receive));
        assert(parser.finish() && frames.size() == 2);
        assert(frames[0].id == "1" && frames[0].event == "text.delta" && frames[0].data == "{\"text\":\"你好😀\"}");
        assert(frames[1].event == "message" && frames[1].data == "second\nline");
    }
    for (const std::string& invalid : { std::string("\xc0\x80"), std::string("\xed\xa0\x80"), std::string("\xf4\x90\x80\x80"), std::string("\xe4") }) assert(!tabforge::valid_utf8(invalid));
    tabforge::SseParser invalid; const std::string bad = "data: \xed\xa0\x80\n\n";
    assert(!invalid.feed(reinterpret_cast<const std::uint8_t*>(bad.data()), bad.size(), [](const auto&) { return true; }) && invalid.error == "invalid_utf8");
    tabforge::SseParser bounded(8); const std::string large = "data: 123456789";
    assert(!bounded.feed(reinterpret_cast<const std::uint8_t*>(large.data()), large.size(), [](const auto&) { return true; }) && bounded.error == "frame_too_large");
    tabforge::SseParser partial; const std::string unfinished = "data: \xe4";
    assert(partial.feed(reinterpret_cast<const std::uint8_t*>(unfinished.data()), unfinished.size(), [](const auto&) { return true; }) && !partial.finish());
    assert(tabforge::next_sequence("18446744073709551615") == "18446744073709551616");
    for (const std::string& json : { std::string("{\"x\":1,\"\\u0078\":2}"), std::string("[1,]"), std::string("{'x':1}"), std::string("{\"x\":NaN}"), std::string("{} []"), std::string("{\"x\":\"\\ud800\"}") }) assert(!tabforge::JsonSyntax(json).valid());
    for (const std::string& json : { std::string("{\"x\":[1,true,null,\"你好😀\"],\"n\":-1.2e+3}"), std::string("\"\\ud83d\\ude00\""), std::string("18446744073709551615") }) assert(tabforge::JsonSyntax(json).valid());
    // Budgets: depth counts container nesting (root 0); nodes count every value.
    assert(tabforge::json_max_depth == 64 && tabforge::protocol_max_nodes == 100000 && tabforge::data_max_nodes == 10000000);
    { const std::string ok = nested(64), deep = nested(65); assert(tabforge::JsonSyntax(ok, 64, tabforge::data_max_nodes).valid()); assert(!tabforge::JsonSyntax(deep, 64, tabforge::data_max_nodes).valid()); }
    { const std::string ten = "[0,0,0,0,0,0,0,0,0]"; assert(tabforge::JsonSyntax(ten, 64, 10).valid()); assert(!tabforge::JsonSyntax(ten, 64, 9).valid()); }
    {
        std::string table = "{\"items\":[";
        for (int i = 0; i < 20000; ++i) table += std::string(i ? "," : "") + "{\"id\":1,\"name\":\"x\",\"ownerId\":\"1\",\"levels\":[1,2,3],\"price\":1.5}";
        table += "]}";
        tabforge::JsonSyntax network(table), data(table, tabforge::json_max_depth, tabforge::data_max_nodes);
        assert(!network.valid()); assert(data.valid() && data.nodes() == 180002);
    }
    {
        const std::string text = "{\"a\":[1.0,1e2,18446744073709551615,-0],\"\\u0062\":\"x\",\"c\":true,\"d\":null}";
        Recorder recorder; assert(tabforge::JsonSyntax(text, 64, 100000, &recorder).valid());
        const std::vector<std::string> expected { "{", "k:a", "[", "n:1.0", "n:1e2", "n:18446744073709551615", "n:-0", "]", "k:b", "s:x", "k:c", "true", "k:d", "null", "}" };
        assert(recorder.events == expected);
    }
    std::cout << "Native SSE byte parser tests passed (not Unreal engine validation)\n";
}
