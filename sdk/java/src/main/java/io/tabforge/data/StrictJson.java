package io.tabforge.data;

import com.google.gson.*;
import com.google.gson.stream.*;
import java.io.*;
import java.math.BigInteger;
import java.nio.*;
import java.nio.charset.*;
import java.util.regex.Pattern;

final class StrictJson {
  /**
   * Data bundle budget shared by every TabForge data loader (Go, Node.js, Java, Python, Unity,
   * Godot, Unreal): container nesting depth with the root value at depth 0, and the number of JSON
   * values (scalars and containers, not keys). Network messages in the client SDKs use a
   * 100,000-value budget instead; they are also capped at 1 MiB.
   */
  static final int MAX_DEPTH = 64;

  static final int MAX_NODES = 10_000_000;

  private static final Pattern PLAIN_INTEGER = Pattern.compile("-?(?:0|[1-9][0-9]*)");

  /**
   * A number that is not a plain integer literal (e.g. 1.0, 1e2). Keeps its exact source text,
   * because ProtoJSON integer rules are decided on the text. Never escapes the validator.
   */
  static final class RawNumber extends Number {
    private static final long serialVersionUID = 1L;
    final String text;

    RawNumber(String text) {
      this.text = text;
    }

    @Override
    public int intValue() {
      return (int) doubleValue();
    }

    @Override
    public long longValue() {
      return (long) doubleValue();
    }

    @Override
    public float floatValue() {
      return (float) doubleValue();
    }

    @Override
    public double doubleValue() {
      return Double.parseDouble(text);
    }

    @Override
    public String toString() {
      return text;
    }
  }

  static JsonElement parse(byte[] bytes) throws IOException {
    String text =
        StandardCharsets.UTF_8
            .newDecoder()
            .onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT)
            .decode(ByteBuffer.wrap(bytes))
            .toString();
    try (JsonReader reader = new JsonReader(new StringReader(text))) {
      reader.setStrictness(Strictness.STRICT);
      JsonElement value = value(reader, 0, new int[] {0});
      if (reader.peek() != JsonToken.END_DOCUMENT)
        throw new IOException("Expected exactly one JSON value");
      return value;
    }
  }

  private static JsonElement value(JsonReader reader, int depth, int[] nodes) throws IOException {
    if (depth > MAX_DEPTH || ++nodes[0] > MAX_NODES)
      throw new IOException("JSON exceeds depth " + MAX_DEPTH + " or " + MAX_NODES + " values");
    switch (reader.peek()) {
      case BEGIN_OBJECT:
        JsonObject object = new JsonObject();
        reader.beginObject();
        while (reader.hasNext()) {
          String key = reader.nextName();
          if (object.has(key)) throw new IOException("Duplicate JSON member: " + key);
          object.add(key, value(reader, depth + 1, nodes));
        }
        reader.endObject();
        return object;
      case BEGIN_ARRAY:
        JsonArray array = new JsonArray();
        reader.beginArray();
        while (reader.hasNext()) array.add(value(reader, depth + 1, nodes));
        reader.endArray();
        return array;
      case STRING:
        return new JsonPrimitive(reader.nextString());
      case NUMBER:
        String number = reader.nextString();
        // Same bound as Gson's own number parsing; BigInteger parsing is superlinear.
        if (number.length() > 10000) throw new IOException("JSON number is too long");
        return new JsonPrimitive(
            PLAIN_INTEGER.matcher(number).matches()
                ? new BigInteger(number)
                : new RawNumber(number));
      case BOOLEAN:
        return new JsonPrimitive(reader.nextBoolean());
      case NULL:
        reader.nextNull();
        return JsonNull.INSTANCE;
      default:
        throw new IOException("Invalid JSON token");
    }
  }

  static JsonObject object(JsonElement value) {
    if (value == null || !value.isJsonObject())
      throw new IllegalArgumentException("Expected object");
    return value.getAsJsonObject();
  }

  static String string(JsonElement value) {
    if (value == null || !value.isJsonPrimitive() || !value.getAsJsonPrimitive().isString())
      throw new IllegalArgumentException("Expected string");
    return value.getAsString();
  }
}
