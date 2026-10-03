#include "TabForgeSse.h"
#include "TabForgeJsonSyntax.h"
#include <cassert>
#include <iostream>
#include <vector>

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
    std::cout << "Native SSE byte parser tests passed (not Unreal engine validation)\n";
}
