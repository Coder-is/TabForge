#pragma once
#include <cstddef>
#include <cstdint>
#include <functional>
#include <string>
#include <utility>

// No Unreal dependency: this byte parser is also compiled by native CI tests.
namespace tabforge {
struct SseFrame { std::string event, id, data; };
inline bool valid_utf8(const std::string& text) {
    for (std::size_t i = 0; i < text.size();) {
        const auto first = static_cast<unsigned char>(text[i]);
        int count = 0;
        if (first <= 0x7f) count = 1;
        else if (first >= 0xc2 && first <= 0xdf) count = 2;
        else if (first >= 0xe0 && first <= 0xef) count = 3;
        else if (first >= 0xf0 && first <= 0xf4) count = 4;
        if (!count || i + count > text.size()) return false;
        std::uint32_t code = first & (count == 1 ? 0x7f : (1u << (7 - count)) - 1);
        for (int j = 1; j < count; ++j) {
            const auto next = static_cast<unsigned char>(text[i + j]);
            if ((next & 0xc0) != 0x80) return false;
            code = (code << 6) | (next & 0x3f);
        }
        if ((count == 2 && code < 0x80) || (count == 3 && code < 0x800) || (count == 4 && code < 0x10000) || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff)) return false;
        i += count;
    }
    return true;
}
class SseParser {
    std::string line_, data_, name_, id_;
    bool first_ = true, skip_lf_ = false, has_data_ = false;
    std::size_t size_ = 0, maximum_;
    bool consume(const std::function<bool(const SseFrame&)>& receive) {
        std::string line;
        line.swap(line_);
        if (!valid_utf8(line)) {
            error = "invalid_utf8";
            return false;
        }
        if (first_) {
            first_ = false;
            if (line.compare(0, 3, "\xef\xbb\xbf") == 0) line.erase(0, 3);
        }
        if (line.empty()) {
            const bool dispatch = has_data_;
            if (dispatch) data_.pop_back();
            SseFrame frame { name_.empty() ? "message" : name_, id_, std::move(data_) };
            data_.clear();
            name_.clear();
            has_data_ = false;
            size_ = 0;
            return !dispatch || receive(frame);
        }
        if (line[0] == ':') return true;
        const auto colon = line.find(':');
        const auto field = line.substr(0, colon);
        auto value = colon == std::string::npos ? std::string() : line.substr(colon + 1);
        if (!value.empty() && value[0] == ' ') value.erase(0, 1);
        if (field == "data") {
            data_ += value;
            data_ += '\n';
            has_data_ = true;
        }
        else if (field == "event") name_ = value;
        else if (field == "id" && value.find('\0') == std::string::npos) id_ = value;
        return true;
    }
public:
    std::string error;
    explicit SseParser(std::size_t maximum_bytes = 1048576) : maximum_(maximum_bytes) {}
    bool feed(const std::uint8_t* bytes, std::size_t count, const std::function<bool(const SseFrame&)>& receive) {
        if (!error.empty()) return false;
        for (std::size_t i = 0; i < count; ++i) {
            const char ch = static_cast<char>(bytes[i]);
            if (skip_lf_) {
                skip_lf_ = false;
                if (ch == '\n') continue;
            }
            if (++size_ > maximum_) {
                error = "frame_too_large";
                return false;
            }
            if (ch == '\r' || ch == '\n') {
                if (!consume(receive)) return false;
                skip_lf_ = ch == '\r';
            }
            else line_ += ch;
        }
        return true;
    }
    bool finish() {
        if (!valid_utf8(line_)) error = "invalid_utf8";
        return error.empty();
    }
};
inline std::string next_sequence(std::string sequence) {
    for (std::size_t i = sequence.size(); i > 0; --i) {
        if (sequence[i - 1] != '9') {
            ++sequence[i - 1];
            return sequence;
        }
        sequence[i - 1] = '0';
    }
    return "1" + sequence;
}
}
