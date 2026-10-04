package io.tabforge.data;

import com.google.gson.*;
import java.math.*;
import java.time.*;
import java.util.*;

final class SchemaValidator {
  private final JsonObject schema;

  SchemaValidator(JsonObject schema) {
    this.schema = schema;
  }

  void validate(String message, JsonElement value) {
    new Validation().message(message, value, "$", 0);
  }

  private final class Validation {
    int nodes;

    void fail(String path, String expected) {
      throw new IllegalArgumentException("Invalid ProtoJSON at " + path + ": expected " + expected);
    }

    void budget(String path, int depth) {
      if (depth > 64 || ++nodes > 100000) fail(path, "bounded message depth/size");
    }

    boolean isString(JsonElement value) {
      return value != null && value.isJsonPrimitive() && value.getAsJsonPrimitive().isString();
    }

    boolean isNumber(JsonElement value) {
      return value != null && value.isJsonPrimitive() && value.getAsJsonPrimitive().isNumber();
    }

    boolean integerString(String value, boolean signed, int bits) {
      if (!value.matches("-?(0|[1-9][0-9]*)") || value.equals("-0") || value.length() > 21)
        return false;
      BigInteger number = new BigInteger(value),
          bound = BigInteger.ONE.shiftLeft(signed ? bits - 1 : bits);
      return number.compareTo(signed ? bound.negate() : BigInteger.ZERO) >= 0
          && number.compareTo(bound) < 0;
    }

    JsonObject object(JsonElement value, String path) {
      if (value == null || !value.isJsonObject()) fail(path, "object");
      return value.getAsJsonObject();
    }

    boolean flag(JsonObject shape, String key) {
      return shape.has(key) && shape.get(key).getAsBoolean();
    }

    void scalar(JsonObject field, JsonElement value, String path, int depth) {
      budget(path, depth);
      String kind = field.get("kind").getAsString();
      switch (kind) {
        case "message":
        case "group":
          message(field.get("type").getAsString(), value, path, depth + 1);
          return;
        case "enum":
          String type = field.get("type").getAsString();
          if (type.equals("google.protobuf.NullValue") && value.isJsonNull()) return;
          if (isNumber(value)) {
            try {
              BigInteger number = value.getAsBigDecimal().toBigIntegerExact();
              if (integerString(number.toString(), true, 32)) return;
            } catch (ArithmeticException ignored) {
            }
          }
          if (isString(value) && schema.getAsJsonObject("enums").has(type))
            for (JsonElement item : schema.getAsJsonObject("enums").getAsJsonArray(type))
              if (item.getAsString().equals(value.getAsString())) return;
          fail(path, "enum name or int32");
          return;
        case "int64":
        case "sint64":
        case "sfixed64":
        case "uint64":
        case "fixed64":
          if (!isString(value)
              || !integerString(
                  value.getAsString(), !kind.equals("uint64") && !kind.equals("fixed64"), 64))
            fail(path, kind + " decimal string");
          return;
        case "int32":
        case "sint32":
        case "sfixed32":
        case "uint32":
        case "fixed32":
          if (isNumber(value))
            try {
              if (integerString(
                  value.getAsBigDecimal().toBigIntegerExact().toString(),
                  !kind.equals("uint32") && !kind.equals("fixed32"),
                  32)) return;
            } catch (ArithmeticException ignored) {
            }
          fail(path, kind);
          return;
        case "float":
        case "double":
          if (isString(value)
              && Set.of("NaN", "Infinity", "-Infinity").contains(value.getAsString())) return;
          if (isNumber(value)
              && Double.isFinite(value.getAsDouble())
              && (!kind.equals("float") || Math.abs(value.getAsDouble()) <= 3.4028234663852886e38))
            return;
          fail(path, kind);
          return;
        case "bool":
          if (!value.isJsonPrimitive() || !value.getAsJsonPrimitive().isBoolean())
            fail(path, "boolean");
          return;
        case "bytes":
        case "string":
          if (!isString(value)) fail(path, "string");
          if (kind.equals("bytes")
              && !value
                  .getAsString()
                  .matches("(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?"))
            fail(path, "padded Base64");
          return;
        default:
          fail(path, "supported scalar type");
      }
    }

    void json(JsonElement value, String path, int depth) {
      budget(path, depth);
      if (value.isJsonArray()) {
        for (JsonElement item : value.getAsJsonArray()) json(item, path, depth + 1);
      } else if (value.isJsonObject()) {
        for (var entry : value.getAsJsonObject().entrySet())
          json(entry.getValue(), path + "." + entry.getKey(), depth + 1);
      } else if (isNumber(value) && !Double.isFinite(value.getAsDouble()))
        fail(path, "finite JSON number");
    }

    boolean wellKnown(String name, JsonElement value, String path, int depth) {
      String prefix = "google.protobuf.";
      if (!name.startsWith(prefix)) return false;
      String shortName = name.substring(prefix.length());
      Map<String, String> wrappers =
          Map.of(
              "DoubleValue",
              "double",
              "FloatValue",
              "float",
              "Int64Value",
              "int64",
              "UInt64Value",
              "uint64",
              "Int32Value",
              "int32",
              "UInt32Value",
              "uint32",
              "BoolValue",
              "bool",
              "StringValue",
              "string",
              "BytesValue",
              "bytes");
      if (wrappers.containsKey(shortName)) {
        JsonObject field = new JsonObject();
        field.addProperty("kind", wrappers.get(shortName));
        scalar(field, value, path, depth + 1);
        return true;
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
          return true;
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
          return true;
        case "FieldMask":
          if (!isString(value)
              || !value
                  .getAsString()
                  .matches(
                      "(?:[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*)*)?"))
            fail(path, "canonical field mask");
          return true;
        case "Struct":
          object(value, path);
          json(value, path, depth + 1);
          return true;
        case "ListValue":
          if (!value.isJsonArray()) fail(path, "array");
          json(value, path, depth + 1);
          return true;
        case "Value":
          json(value, path, depth + 1);
          return true;
        case "Any":
          JsonObject fields = object(value, path);
          if (!isString(fields.get("@type"))) fail(path, "Any @type");
          String url = fields.get("@type").getAsString(),
              nested = url.substring(url.lastIndexOf('/') + 1);
          if (nested.equals(name) || !schema.getAsJsonObject("messages").has(nested))
            fail(path, "known Any type");
          JsonObject copy = fields.deepCopy();
          copy.remove("@type");
          if (nested.startsWith(prefix) && !nested.equals(prefix + "Empty")) {
            if (copy.size() != 1 || !copy.has("value")) fail(path, "Any value");
            message(nested, copy.get("value"), path + ".value", depth + 1);
          } else message(nested, copy, path, depth + 1);
          return true;
        default:
          return false;
      }
    }

    void message(String name, JsonElement value, String path, int depth) {
      budget(path, depth);
      JsonObject messages = schema.getAsJsonObject("messages");
      if (!messages.has(name)) fail(path, "known message type");
      if (wellKnown(name, value, path, depth)) return;
      JsonObject shape = messages.getAsJsonObject(name),
          fields = object(value, path),
          definitions = shape.getAsJsonObject("fields");
      if (shape.has("oneofs"))
        for (JsonElement group : shape.getAsJsonArray("oneofs")) {
          int count = 0;
          for (JsonElement key : group.getAsJsonArray()) if (fields.has(key.getAsString())) count++;
          if (count > 1) fail(path, "at most one oneof member");
        }
      for (var entry : definitions.entrySet())
        if (flag(entry.getValue().getAsJsonObject(), "required") && !fields.has(entry.getKey()))
          fail(path + "." + entry.getKey(), "required field");
      for (var entry : fields.entrySet()) {
        String next = path + "." + entry.getKey();
        if (!definitions.has(entry.getKey())) fail(next, "declared field");
        JsonObject field = definitions.getAsJsonObject(entry.getKey());
        JsonElement item = entry.getValue();
        if (field.has("mapKey")) {
          String kind = field.get("mapKey").getAsString();
          for (var pair : object(item, next).entrySet()) {
            if (kind.equals("bool") && !Set.of("true", "false").contains(pair.getKey()))
              fail(next, "boolean map key");
            if (!kind.equals("string")
                && !kind.equals("bool")
                && !integerString(
                    pair.getKey(),
                    kind.startsWith("int") || kind.startsWith("s"),
                    kind.endsWith("32") ? 32 : 64)) fail(next, "integer map key");
            scalar(field, pair.getValue(), next + "." + pair.getKey(), depth + 1);
          }
        } else if (flag(field, "list")) {
          if (!item.isJsonArray()) fail(next, "array");
          int index = 0;
          for (JsonElement element : item.getAsJsonArray())
            scalar(field, element, next + "[" + (index++) + "]", depth + 1);
        } else scalar(field, item, next, depth + 1);
      }
    }
  }
}
