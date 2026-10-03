#pragma once
#include "TabForgeSse.h"
#include <unordered_set>

namespace tabforge {
// Reject duplicate keys, excessive nesting and non-JSON extensions before an
// engine JSON library materializes values or silently overwrites fields.
class JsonSyntax {
    const std::string& text_;
    std::size_t pos_ = 0, nodes_ = 0;
    void space() { while (pos_ < text_.size() && (text_[pos_] == ' ' || text_[pos_] == '\t' || text_[pos_] == '\r' || text_[pos_] == '\n')) ++pos_; }
    bool take(char ch) { space(); if (pos_ >= text_.size() || text_[pos_] != ch) return false; ++pos_; return true; }
    static void utf8(std::string& out, std::uint32_t code) {
        if (code < 0x80) out += static_cast<char>(code);
        else if (code < 0x800) { out += static_cast<char>(0xc0 | (code >> 6)); out += static_cast<char>(0x80 | (code & 0x3f)); }
        else if (code < 0x10000) { out += static_cast<char>(0xe0 | (code >> 12)); out += static_cast<char>(0x80 | ((code >> 6) & 0x3f)); out += static_cast<char>(0x80 | (code & 0x3f)); }
        else { out += static_cast<char>(0xf0 | (code >> 18)); out += static_cast<char>(0x80 | ((code >> 12) & 0x3f)); out += static_cast<char>(0x80 | ((code >> 6) & 0x3f)); out += static_cast<char>(0x80 | (code & 0x3f)); }
    }
    bool hex(std::uint32_t& code) {
        code = 0;
        for (int i = 0; i < 4; ++i) {
            if (pos_ == text_.size()) return false;
            const char ch = text_[pos_++];
            const int digit = ch >= '0' && ch <= '9' ? ch - '0' : ch >= 'a' && ch <= 'f' ? ch - 'a' + 10 : ch >= 'A' && ch <= 'F' ? ch - 'A' + 10 : -1;
            if (digit < 0) return false; code = (code << 4) | digit;
        }
        return true;
    }
    bool string(std::string& out) {
        if (!take('"')) return false;
        while (pos_ < text_.size()) {
            const auto ch = static_cast<unsigned char>(text_[pos_++]);
            if (ch == '"') return true;
            if (ch < 0x20) return false;
            if (ch != '\\') { out += static_cast<char>(ch); continue; }
            if (pos_ == text_.size()) return false;
            const char escape = text_[pos_++];
            if (escape == '"' || escape == '\\' || escape == '/') out += escape;
            else if (escape == 'b') out += '\b'; else if (escape == 'f') out += '\f'; else if (escape == 'n') out += '\n'; else if (escape == 'r') out += '\r'; else if (escape == 't') out += '\t';
            else if (escape == 'u') {
                std::uint32_t code; if (!hex(code)) return false;
                if (code >= 0xd800 && code <= 0xdbff) {
                    if (pos_ + 2 > text_.size() || text_[pos_++] != '\\' || text_[pos_++] != 'u') return false;
                    std::uint32_t low; if (!hex(low) || low < 0xdc00 || low > 0xdfff) return false;
                    code = 0x10000 + ((code - 0xd800) << 10) + low - 0xdc00;
                } else if (code >= 0xdc00 && code <= 0xdfff) return false;
                utf8(out, code);
            } else return false;
        }
        return false;
    }
    bool digits() { const auto start = pos_; while (pos_ < text_.size() && text_[pos_] >= '0' && text_[pos_] <= '9') ++pos_; return pos_ != start; }
    bool value(std::size_t depth) {
        space(); if (depth > 64 || ++nodes_ > 100000 || pos_ == text_.size()) return false;
        if (text_[pos_] == '"') { std::string out; return string(out); }
        if (take('{')) {
            std::unordered_set<std::string> keys;
            if (take('}')) return true;
            do { std::string key; if (!string(key) || !keys.insert(key).second || !take(':') || !value(depth + 1)) return false; if (take('}')) return true; } while (take(','));
            return false;
        }
        if (take('[')) { if (take(']')) return true; do { if (!value(depth + 1)) return false; if (take(']')) return true; } while (take(',')); return false; }
        for (const char* word : { "true", "false", "null" }) { const std::string literal(word); if (text_.compare(pos_, literal.size(), literal) == 0) { pos_ += literal.size(); return true; } }
        if (text_[pos_] == '-') ++pos_;
        if (pos_ == text_.size()) return false;
        if (text_[pos_] == '0') ++pos_; else { if (text_[pos_] < '1' || text_[pos_] > '9' || !digits()) return false; }
        if (pos_ < text_.size() && text_[pos_] == '.') { ++pos_; if (!digits()) return false; }
        if (pos_ < text_.size() && (text_[pos_] == 'e' || text_[pos_] == 'E')) { ++pos_; if (pos_ < text_.size() && (text_[pos_] == '+' || text_[pos_] == '-')) ++pos_; if (!digits()) return false; }
        return true;
    }
public:
    explicit JsonSyntax(const std::string& text) : text_(text) {}
    bool valid() { if (!valid_utf8(text_) || !value(0)) return false; space(); return pos_ == text_.size(); }
};
}
