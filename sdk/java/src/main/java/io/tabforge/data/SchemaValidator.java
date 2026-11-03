package io.tabforge.data;

import com.google.gson.*;
import java.math.*;
import java.util.*;
import java.util.concurrent.ConcurrentHashMap;
import java.util.regex.*;

/**
 * Validates ProtoJSON with Go protojson's input rules and rewrites the tree in place to canonical
 * ProtoJSON. Depth equals the JSON nesting depth and every JSON value counts at most once, so for
 * parsed text the parser's budget is always the binding one.
 */
final class SchemaValidator {
  private static final String PREFIX = "google.protobuf.";
  private static final Pattern JSON_NUMBER =
      Pattern.compile("-?(0|[1-9][0-9]*)(?:\\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?");
  private static final Pattern MAP_INTEGER = Pattern.compile("([+-]?)([0-9]+)");
  private static final String BASE64 =
      "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  private static final Map<String, String> WRAPPERS =
      Map.of(
          "DoubleValue", "double",
          "FloatValue", "float",
          "Int64Value", "int64",
          "UInt64Value", "uint64",
          "Int32Value", "int32",
          "UInt32Value", "uint32",
          "BoolValue", "bool",
          "StringValue", "string",
          "BytesValue", "bytes");

  private static final Pattern TIMESTAMP =
      Pattern.compile(
          "([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:([.,])([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))");
  private static final Pattern DURATION = Pattern.compile("[+-]?(0|[1-9][0-9]*)?(\\.[0-9]{0,9})?s");
  // 2^128 - 2^104 is the largest float32; decimals from 2^128 - 2^103 up overflow it.
  private static final double FLOAT32_LIMIT = 3.4028235677973366e38;
  private static final BigDecimal FLOAT32_HALFWAY =
      new BigDecimal("340282356779733661637539395458142568448");

  private final JsonObject schema;
  // Message name -> original Proto field name -> JSON name.
  private final Map<String, Map<String, String>> aliases = new ConcurrentHashMap<>();

  SchemaValidator(JsonObject schema) {
    this.schema = schema;
  }

  // Days from 1970-01-01, proleptic Gregorian; year 0 is allowed (offsets can reach year 1).
  private static long civilDays(int year, int month, int day) {
    long y = year + 400 - (month <= 2 ? 1 : 0), era = y / 400, yoe = y - era * 400;
    long doy = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
    return era * 146097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719468 - 146097;
  }

  private static int daysInMonth(int year, int month) {
    if (month == 2) return year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) ? 29 : 28;
    return new int[] {31, 0, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[month - 1];
  }

  /**
   * Go protojson Timestamp input: time.Parse(time.RFC3339Nano), then 0001-01-01 to 9999-12-31 in
   * UTC and at most 9 digits after ".". Like Go: any offset (hours to 24, minutes to 60), a
   * one-digit hour, and "," before a fraction of any length.
   */
  static boolean protoTimestamp(String value) {
    Matcher match = TIMESTAMP.matcher(value);
    if (!match.matches()) return false;
    int year = Integer.parseInt(match.group(1)),
        month = Integer.parseInt(match.group(2)),
        day = Integer.parseInt(match.group(3)),
        hour = Integer.parseInt(match.group(4)),
        minute = Integer.parseInt(match.group(5)),
        second = Integer.parseInt(match.group(6));
    if ((".".equals(match.group(7)) && match.group(8).length() > 9)
        || month < 1
        || month > 12
        || day < 1
        || day > daysInMonth(year, month)
        || hour > 23
        || minute > 59
        || second > 59) return false;
    long offset = 0;
    if (match.group(9) != null) {
      int hours = Integer.parseInt(match.group(10)), minutes = Integer.parseInt(match.group(11));
      if (hours > 24 || minutes > 60) return false;
      offset = (match.group(9).equals("-") ? -1 : 1) * (hours * 3600L + minutes * 60L);
    }
    long seconds =
        civilDays(year, month, day) * 86400L + hour * 3600L + minute * 60L + second - offset;
    return seconds >= -62135596800L && seconds <= 253402300799L;
  }

  /**
   * Go protojson Duration input: optional sign, integer and/or "." with up to 9 digits ("1.s",
   * ".5s" and ".s" are accepted), then "s", within 315576000000 seconds.
   */
  static boolean protoDuration(String value) {
    Matcher match = DURATION.matcher(value);
    if (!match.matches() || (match.group(1) == null && match.group(2) == null)) return false;
    String seconds = match.group(1) == null ? "" : match.group(1);
    return seconds.length() < 12
        || (seconds.length() == 12 && seconds.compareTo("315576000000") <= 0);
  }

  /**
   * Go's float32 value of finite JSON number text, rounded once from the exact decimal, or null
   * when it overflows float32 (3.40282356e+38 becomes 3.4028235e+38, 1e-50 becomes 0).
   */
  static Double float32(double number, String text) {
    if (Math.abs(number) == FLOAT32_LIMIT)
      return new BigDecimal(text).abs().compareTo(FLOAT32_HALFWAY) < 0
          ? Math.copySign((double) Float.MAX_VALUE, number)
          : null;
    if (Math.abs(number) > FLOAT32_LIMIT) return null;
    return (double) (float) number;
  }

  /** Returns the canonical value; containers are normalized in place. */
  JsonElement validate(String message, JsonElement value) {
    return new Validation().message(message, value, "$", 0, false, false);
  }

  private static boolean signedKind(String kind) {
    return kind.startsWith("int") || kind.startsWith("s");
  }

  private static int bits(String kind) {
    return kind.endsWith("32") ? 32 : 64;
  }

  private static boolean integerKind(String kind) {
    return Set.of(
            "int32", "sint32", "sfixed32", "uint32", "fixed32", "int64", "sint64", "sfixed64",
            "uint64", "fixed64")
        .contains(kind);
  }

  private static boolean inRange(String digits, boolean negative, boolean signed, int bits) {
    if (negative && !signed) return false;
    String maximum =
        bits == 32
            ? (signed ? (negative ? "2147483648" : "2147483647") : "4294967295")
            : (signed
                ? (negative ? "9223372036854775808" : "9223372036854775807")
                : "18446744073709551615");
    return digits.length() < maximum.length()
        || (digits.length() == maximum.length() && digits.compareTo(maximum) <= 0);
  }

  private static String stripZeros(String digits) {
    int start = 0;
    while (start < digits.length() - 1 && digits.charAt(start) == '0') start++;
    return digits.substring(start);
  }

  /**
   * Go protojson integer input (normalizeToIntString): JSON number text that is exact after
   * shifting the exponent, including Go's 20-digit cap. Returns the canonical decimal or null.
   */
  static String protoInteger(String text, boolean signed, int bits) {
    Matcher match = JSON_NUMBER.matcher(text);
    if (!match.matches()) return null;
    String intp = match.group(1).equals("0") ? "" : match.group(1);
    String frac = match.group(2) == null ? "" : match.group(2).replaceAll("0+$", "");
    if (intp.isEmpty() && frac.isEmpty()) return "0";
    int exponent = 0;
    if (match.group(3) != null) {
      try {
        exponent = Integer.parseInt(match.group(3));
      } catch (NumberFormatException error) {
        return null;
      }
    }
    String digits;
    if (exponent >= 0) {
      if (frac.length() > exponent || (long) intp.length() + exponent > 20) return null;
      digits = intp + frac + "0".repeat(exponent - frac.length());
    } else {
      long index = (long) intp.length() + exponent;
      if (!frac.isEmpty() || index < 0) return null;
      for (int i = (int) index; i < intp.length(); i++) if (intp.charAt(i) != '0') return null;
      digits = intp.substring(0, (int) index);
    }
    digits = stripZeros(digits);
    boolean negative = text.startsWith("-");
    return inRange(digits, negative, signed, bits) ? (negative ? "-" : "") + digits : null;
  }

  /** Go strconv.ParseInt/ParseUint(key, 10, bits) for map keys; returns the canonical key. */
  static String protoMapKey(String key, String kind) {
    if (kind.equals("string")) return key;
    if (kind.equals("bool")) return key.equals("true") || key.equals("false") ? key : null;
    if (!integerKind(kind)) return null;
    Matcher match = MAP_INTEGER.matcher(key);
    boolean signed = signedKind(kind);
    if (!match.matches() || (!match.group(1).isEmpty() && !signed)) return null;
    String digits = stripZeros(match.group(2));
    boolean negative = match.group(1).equals("-") && !digits.equals("0");
    return inRange(digits, negative, signed, bits(kind)) ? (negative ? "-" : "") + digits : null;
  }

  /**
   * Go protojson bytes input: URL alphabet when "-" or "_" occurs, padded exactly when the length
   * is a multiple of 4, CR/LF ignored, unused bits ignored. Returns padded standard Base64.
   */
  static String protoBytes(String value) {
    boolean url = value.indexOf('-') >= 0 || value.indexOf('_') >= 0;
    String text = value.replace("\r", "").replace("\n", "");
    String alphabet = url ? "[A-Za-z0-9_-]" : "[A-Za-z0-9+/]";
    String pattern =
        value.length() % 4 == 0
            ? "(?:" + alphabet + "{4})*(?:" + alphabet + "{2}==|" + alphabet + "{3}=)?"
            : "(?:" + alphabet + "{4})*(?:" + alphabet + "{2,3})?";
    if (!text.matches(pattern)) return null;
    String body = text.replaceAll("=+$", "").replace('-', '+').replace('_', '/');
    int rest = body.length() % 4;
    if (rest != 0) {
      int last = BASE64.indexOf(body.charAt(body.length() - 1)) & (rest == 2 ? 0x30 : 0x3c);
      body = body.substring(0, body.length() - 1) + BASE64.charAt(last) + (rest == 2 ? "==" : "=");
    }
    return body;
  }

  private final class Validation {
    int nodes;

    void fail(String path, String expected) {
      throw new IllegalArgumentException("Invalid ProtoJSON at " + path + ": expected " + expected);
    }

    void budget(String path, int depth) {
      if (depth > StrictJson.MAX_DEPTH || ++nodes > StrictJson.MAX_NODES)
        fail(path, "bounded message depth/size");
    }

    boolean isString(JsonElement value) {
      return value != null && value.isJsonPrimitive() && value.getAsJsonPrimitive().isString();
    }

    boolean isNumber(JsonElement value) {
      return value != null && value.isJsonPrimitive() && value.getAsJsonPrimitive().isNumber();
    }

    String numberText(JsonElement value) {
      if (isString(value)) return value.getAsString();
      return isNumber(value) ? value.getAsNumber().toString() : null;
    }

    JsonObject object(JsonElement value, String path) {
      if (value == null || !value.isJsonObject()) fail(path, "object");
      return value.getAsJsonObject();
    }

    boolean flag(JsonObject shape, String key) {
      return shape.has(key) && shape.get(key).getAsBoolean();
    }

    String type(JsonObject field) {
      return field.has("type") ? field.get("type").getAsString() : "";
    }

    // JSON null means "field absent" except where null is itself a value.
    boolean nullable(JsonObject field) {
      String kind = field.get("kind").getAsString();
      return (kind.equals("message") && type(field).equals(PREFIX + "Value"))
          || (kind.equals("enum") && type(field).equals(PREFIX + "NullValue"));
    }

    JsonElement scalar(
        JsonObject field, JsonElement value, String path, int depth, boolean counted) {
      String kind = field.get("kind").getAsString();
      if (kind.equals("message") || kind.equals("group"))
        return message(type(field), value, path, depth, counted, false);
      if (!counted) budget(path, depth);
      if (kind.equals("enum")) {
        String type = type(field);
        if (type.equals(PREFIX + "NullValue") && value != null && value.isJsonNull()) return value;
        if (isString(value) && schema.getAsJsonObject("enums").has(type))
          for (JsonElement item : schema.getAsJsonObject("enums").getAsJsonArray(type))
            if (item.getAsString().equals(value.getAsString())) return value;
        String number = isNumber(value) ? protoInteger(numberText(value), true, 32) : null;
        if (number == null) fail(path, "enum name or int32");
        return new JsonPrimitive(Long.parseLong(number));
      }
      if (integerKind(kind)) {
        String text = numberText(value);
        String integer = text == null ? null : protoInteger(text, signedKind(kind), bits(kind));
        if (integer == null) fail(path, kind);
        return bits(kind) == 64 ? new JsonPrimitive(integer) : new JsonPrimitive(Long.parseLong(integer));
      }
      switch (kind) {
        case "float":
        case "double":
          if (isString(value)
              && Set.of("NaN", "Infinity", "-Infinity").contains(value.getAsString())) return value;
          String text = numberText(value);
          if (text == null || !JSON_NUMBER.matcher(text).matches()) fail(path, kind);
          double number = Double.parseDouble(text);
          if (!Double.isFinite(number)) fail(path, kind);
          if (kind.equals("double")) return new JsonPrimitive(number);
          Double rounded = float32(number, text);
          if (rounded == null) fail(path, kind);
          return new JsonPrimitive(rounded);
        case "bool":
          if (value == null || !value.isJsonPrimitive() || !value.getAsJsonPrimitive().isBoolean())
            fail(path, "boolean");
          return value;
        case "string":
          if (!isString(value)) fail(path, "string");
          return value;
        case "bytes":
          String bytes = isString(value) ? protoBytes(value.getAsString()) : null;
          if (bytes == null) fail(path, "Base64 string");
          return new JsonPrimitive(bytes);
        default:
          fail(path, "supported scalar type");
          return value;
      }
    }

    JsonElement json(JsonElement value, String path, int depth, boolean counted) {
      if (!counted) budget(path, depth);
      if (value == null) fail(path, "JSON value");
      if (value.isJsonArray()) {
        JsonArray array = value.getAsJsonArray();
        for (int i = 0; i < array.size(); i++) array.set(i, json(array.get(i), path, depth + 1, false));
      } else if (value.isJsonObject()) {
        JsonObject object = value.getAsJsonObject();
        for (String key : new ArrayList<>(object.keySet()))
          object.add(key, json(object.get(key), path + "." + key, depth + 1, false));
      } else if (isNumber(value)) {
        Number number = value.getAsNumber();
        if (!Double.isFinite(number.doubleValue())) fail(path, "finite JSON number");
        if (number instanceof StrictJson.RawNumber) return new JsonPrimitive(number.doubleValue());
      }
      return value;
    }

    /** Returns null for types without a special JSON form. */
    JsonElement wellKnown(String name, JsonElement value, String path, int depth) {
      String shortName = name.substring(PREFIX.length());
      if (WRAPPERS.containsKey(shortName)) {
        JsonObject field = new JsonObject();
        field.addProperty("kind", WRAPPERS.get(shortName));
        return scalar(field, value, path, depth, true);
      }
      switch (shortName) {
        case "Timestamp":
          if (!isString(value) || !protoTimestamp(value.getAsString()))
            fail(path, "RFC 3339 timestamp in range");
          return value;
        case "Duration":
          if (!isString(value) || !protoDuration(value.getAsString()))
            fail(path, "duration in range");
          return value;
        case "FieldMask":
          if (!isString(value)
              || !value
                  .getAsString()
                  .matches(
                      "(?:[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*)*)?"))
            fail(path, "canonical field mask");
          return value;
        case "Struct":
          JsonObject struct = object(value, path);
          for (String key : new ArrayList<>(struct.keySet()))
            struct.add(key, json(struct.get(key), path + "." + key, depth + 1, false));
          return value;
        case "ListValue":
          if (value == null || !value.isJsonArray()) fail(path, "array");
          JsonArray list = value.getAsJsonArray();
          for (int i = 0; i < list.size(); i++) list.set(i, json(list.get(i), path, depth + 1, false));
          return value;
        case "Value":
          return json(value, path, depth, true);
        case "Any":
          JsonObject fields = object(value, path);
          // protojson writes an unset Any as {} and Empty as {"@type": ..., "value": {}}.
          if (fields.size() == 0) return value;
          if (!isString(fields.get("@type"))) fail(path, "Any @type");
          String url = fields.get("@type").getAsString(),
              nested = url.substring(url.lastIndexOf('/') + 1);
          // An Any may hold another Any; the depth budget bounds the recursion.
          if (!schema.getAsJsonObject("messages").has(nested)) fail(path, "known Any type");
          List<String> members = new ArrayList<>(fields.keySet());
          members.remove("@type");
          // Go tolerates an omitted Empty value on input.
          if (nested.equals(PREFIX + "Empty") && members.isEmpty()) return value;
          if (nested.startsWith(PREFIX)) {
            if (members.size() != 1 || !members.get(0).equals("value")) fail(path, "Any value");
            fields.add(
                "value", message(nested, fields.get("value"), path + ".value", depth + 1, false, false));
          } else message(nested, fields, path, depth, true, true);
          return value;
        default:
          return null;
      }
    }

    JsonElement message(
        String name,
        JsonElement value,
        String path,
        int depth,
        boolean counted,
        boolean skipType) {
      if (!counted) budget(path, depth);
      if (name.startsWith(PREFIX)) {
        JsonElement known = wellKnown(name, value, path, depth);
        if (known != null) return known;
      }
      JsonObject messages = schema.getAsJsonObject("messages");
      if (!messages.has(name)) fail(path, "known message type");
      JsonObject shape = messages.getAsJsonObject(name),
          fields = object(value, path),
          definitions = shape.getAsJsonObject("fields");
      Map<String, String> protoNames = aliases.computeIfAbsent(name, ignored -> protoNameIndex(definitions));
      // A member is spelled as the JSON name or the original Proto name; like Go protojson, a
      // field given twice (also once in each spelling) is rejected.
      List<String[]> members = new ArrayList<>();
      Map<String, String> spelled = new HashMap<>();
      for (String key : new ArrayList<>(fields.keySet())) {
        if (skipType && key.equals("@type")) continue;
        String jsonName = definitions.has(key) ? key : protoNames.get(key);
        if (jsonName == null) fail(path + "." + key, "declared field");
        if (spelled.containsKey(jsonName)) fail(path + "." + key, "field given once");
        spelled.put(jsonName, key);
        members.add(new String[] {key, jsonName});
      }
      if (shape.has("oneofs"))
        for (JsonElement group : shape.getAsJsonArray("oneofs")) {
          int count = 0;
          for (JsonElement key : group.getAsJsonArray())
            if (present(fields, definitions, spelled, key.getAsString())) count++;
          if (count > 1) fail(path, "at most one oneof member");
        }
      for (var entry : definitions.entrySet())
        if (flag(entry.getValue().getAsJsonObject(), "required")
            && !present(fields, definitions, spelled, entry.getKey()))
          fail(path + "." + entry.getKey(), "required field");
      for (String[] member : members) {
        String key = member[0], jsonName = member[1];
        String next = path + "." + key;
        JsonObject field = definitions.getAsJsonObject(jsonName);
        JsonElement item = fields.get(key);
        if (item.isJsonNull() && !nullable(field)) {
          fields.remove(key);
          continue;
        }
        if (field.has("mapKey")) {
          String kind = field.get("mapKey").getAsString();
          JsonObject entries = object(item, next);
          budget(next, depth + 1);
          Map<String, String> keys = new LinkedHashMap<>();
          Set<String> seen = new HashSet<>();
          boolean changed = false;
          for (String entryKey : new ArrayList<>(entries.keySet())) {
            String canonical = protoMapKey(entryKey, kind);
            if (canonical == null) fail(next, kind + " map key");
            if (!seen.add(canonical)) fail(next, "unique map key");
            keys.put(entryKey, canonical);
            changed |= !canonical.equals(entryKey);
            entries.add(
                entryKey,
                scalar(field, entries.get(entryKey), next + "." + entryKey, depth + 2, false));
          }
          if (changed) {
            JsonObject rebuilt = new JsonObject();
            for (var pair : keys.entrySet()) rebuilt.add(pair.getValue(), entries.get(pair.getKey()));
            fields.add(key, rebuilt);
          }
        } else if (flag(field, "list")) {
          if (!item.isJsonArray()) fail(next, "array");
          budget(next, depth + 1);
          JsonArray array = item.getAsJsonArray();
          for (int i = 0; i < array.size(); i++)
            array.set(i, scalar(field, array.get(i), next + "[" + i + "]", depth + 2, false));
        } else fields.add(key, scalar(field, item, next, depth + 1, false));
        if (!key.equals(jsonName)) fields.add(jsonName, fields.remove(key));
      }
      return fields;
    }

    Map<String, String> protoNameIndex(JsonObject definitions) {
      Map<String, String> names = new HashMap<>();
      for (var entry : definitions.entrySet()) {
        JsonElement protoName = entry.getValue().getAsJsonObject().get("protoName");
        if (isString(protoName) && !definitions.has(protoName.getAsString()))
          names.put(protoName.getAsString(), entry.getKey());
      }
      return names;
    }

    boolean present(
        JsonObject fields, JsonObject definitions, Map<String, String> spelled, String name) {
      String key = spelled.get(name);
      return key != null
          && (!fields.get(key).isJsonNull() || nullable(definitions.getAsJsonObject(name)));
    }
  }
}
