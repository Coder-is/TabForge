package io.tabforge.data;

import com.google.gson.*;
import com.google.gson.stream.*;
import java.io.*;
import java.math.BigDecimal;
import java.nio.*;
import java.nio.charset.*;

final class StrictJson {
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
    if (depth > 64 || ++nodes[0] > 100000) throw new IOException("JSON depth/size exceeds budget");
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
        return new JsonPrimitive(new BigDecimal(reader.nextString()));
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
