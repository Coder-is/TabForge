package io.tabforge.data;

import com.google.gson.*;
import java.math.*;
import java.time.*;
import java.util.*;
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

  private final JsonObject schema;

  SchemaValidator(JsonObject schema) {
    this.schema = schema;
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
          // Go emits float32 max as 3.4028235e+38; accept values that round to a finite float32.
          if (!Double.isFinite(number)
              || (kind.equals("float") && Math.abs(number) >= 3.4028235677973366e38))
            fail(path, kind);
          return new JsonPrimitive(number);
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
          if (!isString(value)
              || !value
                  .getAsString()
                  .matches(
                      "[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z")
              || value.getAsString().startsWith("0000")) fail(path, "canonical UTC timestamp");
          try {
            LocalDateTime.parse(value.getAsString().substring(0, 19));
          } catch (DateTimeException error) {
            fail(path, "valid UTC timestamp");
          }
          return value;
        case "Duration":
          if (!isString(value)
              || !value
                  .getAsString()
                  .matches("-?(0|[1-9][0-9]*)(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s"))
            fail(path, "duration in range");
          String seconds =
              value.getAsString().substring(0, value.getAsString().length() - 1).split("\\.")[0];
          if (seconds.length() > 13
              || new BigInteger(seconds).abs().compareTo(new BigInteger("315576000000")) > 0)
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
          if (nested.equals(name) || !schema.getAsJsonObject("messages").has(nested))
            fail(path, "known Any type");
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
      if (shape.has("oneofs"))
        for (JsonElement group : shape.getAsJsonArray("oneofs")) {
          int count = 0;
          for (JsonElement key : group.getAsJsonArray())
            if (present(fields, definitions, key.getAsString())) count++;
          if (count > 1) fail(path, "at most one oneof member");
        }
      for (var entry : definitions.entrySet())
        if (flag(entry.getValue().getAsJsonObject(), "required")
            && !present(fields, definitions, entry.getKey()))
          fail(path + "." + entry.getKey(), "required field");
      for (String key : new ArrayList<>(fields.keySet())) {
        if (skipType && key.equals("@type")) continue;
        String next = path + "." + key;
        if (!definitions.has(key)) fail(next, "declared field");
        JsonObject field = definitions.getAsJsonObject(key);
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
      }
      return fields;
    }

    boolean present(JsonObject fields, JsonObject definitions, String name) {
      return fields.has(name)
          && (!fields.get(name).isJsonNull()
              || !definitions.has(name)
              || nullable(definitions.getAsJsonObject(name)));
    }
  }
}
