extends Node

const Parser = preload("sse_parser.gd")
const Wire = preload("wire_validator.gd")
const UTF8 = preload("utf8.gd")

class Request extends RefCounted:
	signal event_received(event: Dictionary)
	signal completed(value: Variant)
	signal failed(error: Dictionary)
	var done := false
	var result: Variant
	var error: Dictionary = {}
	var request_id := ""
	var _cancelled := false
	var _http := HTTPClient.new()
	var _operation: Dictionary = {}
	var _parser: RefCounted
	var _sequence := "0"
	var _started := 0
	var _sent := false
	var _headers := false
	var _status := 0
	var _buffer := PackedByteArray()
	var _body := ""
	var _request_headers := PackedStringArray()
	func cancel() -> void:
		_cancelled = true

var max_response_bytes := 1048576
var max_frame_bytes := 1048576
var _contract: Dictionary = {}
var _host := ""
var _port := -1
var _tls: TLSOptions
var _base_path := ""
var _configured := false
var _active: Array[Request] = []

# Load the generated protocol.gd CONTRACT constant. No global class registration.
func configure(base_url: String, contract: Dictionary) -> Error:
	if not _active.is_empty(): return ERR_BUSY
	var regex := RegEx.new()
	regex.compile("^(https?)://(\\[[0-9A-Fa-f:]+\\]|[^/:?#@]+)(?::([0-9]+))?(/[^?#]*)?$")
	var match_url := regex.search(base_url)
	if match_url == null or not contract.has_all(["version", "schemaHash", "operations", "wireSchema"]): return ERR_INVALID_PARAMETER
	_host = match_url.get_string(2).trim_prefix("[").trim_suffix("]")
	_port = int(match_url.get_string(3)) if not match_url.get_string(3).is_empty() else (443 if match_url.get_string(1) == "https" else 80)
	if _port < 1 or _port > 65535: return ERR_INVALID_PARAMETER
	_tls = TLSOptions.client() if match_url.get_string(1) == "https" else null
	_base_path = match_url.get_string(4).trim_suffix("/")
	_contract = contract.duplicate(true)
	_configured = true
	return OK

func request(operation_id: String, data: Variant, options: Dictionary = {}) -> Request:
	return _create(operation_id, data, options, "http_json")

func stream(operation_id: String, data: Variant, options: Dictionary = {}) -> Request:
	return _create(operation_id, data, options, "http_sse")

func _create(operation_id: String, data: Variant, options: Dictionary, transport: String) -> Request:
	var handle := Request.new()
	_active.append(handle)
	# Defer all outcomes so the caller can attach signals before errors fire.
	var snapshot: Variant = data.duplicate(true) if data is Dictionary or data is Array else data
	_begin.call_deferred(handle, operation_id, snapshot, options.duplicate(true), transport)
	return handle

func _begin(handle: Request, operation_id: String, data: Variant, options: Dictionary, transport: String) -> void:
	if not is_inside_tree(): _fail(handle, "not_ready", "Client must be in the scene tree"); return
	if handle._cancelled: _fail(handle, "cancelled", "Request cancelled"); return
	if not _configured: _fail(handle, "not_ready", "Configure the generated contract first"); return
	if not _contract.operations.has(operation_id): _fail(handle, "invalid_operation", "Unknown operation"); return
	var operation: Dictionary = _contract.operations[operation_id]
	if operation.transport != transport: _fail(handle, "invalid_operation", "Wrong operation transport"); return
	if transport == "http_sse" and OS.has_feature("web"):
		_fail(handle, "unsupported_transport", "Godot Web requires a fetch streaming bridge"); return
	var validation: String = Wire.new(_contract.wireSchema).validate(operation.requestType, data)
	if not validation.is_empty(): _fail(handle, "invalid_message", validation); return
	if (options.has("token") and not options.token is String) or (options.has("requestId") and not options.requestId is String):
		_fail(handle, "bad_request", "token and requestId must be strings"); return
	var token: String = options.get("token", "")
	if token.contains("\r") or token.contains("\n"): _fail(handle, "bad_request", "Invalid bearer token"); return
	if operation.auth == "bearer" and token.is_empty(): _fail(handle, "unauthorized", "Bearer token required"); return
	var id_regex := RegEx.new()
	id_regex.compile("^[A-Za-z0-9_.-]{1,128}$")
	handle.request_id = options.get("requestId", Crypto.new().generate_random_bytes(16).hex_encode())
	if id_regex.search(handle.request_id) == null: _fail(handle, "bad_request", "Invalid request ID"); return
	handle._operation = operation
	handle._parser = Parser.new()
	handle._parser.maximum_bytes = max_frame_bytes
	handle._body = JSON.stringify(data)
	handle._request_headers = PackedStringArray([
		"Content-Type: application/json", "Accept: text/event-stream" if transport == "http_sse" else "Accept: application/json",
		"X-Protocol-Version: " + _contract.version, "X-Protocol-Schema: " + _contract.schemaHash, "X-Request-ID: " + handle.request_id
	])
	if not token.is_empty(): handle._request_headers.append("Authorization: Bearer " + token)
	handle._started = Time.get_ticks_msec()
	var connect_error := handle._http.connect_to_host(_host, _port, _tls)
	if connect_error != OK: _fail(handle, "transport_error", "Cannot connect to host"); return

func _process(_delta: float) -> void:
	for handle in _active.duplicate():
		_poll(handle)
		if handle.done: _active.erase(handle)

func _exit_tree() -> void:
	for handle in _active:
		_fail(handle, "cancelled", "Client left the scene tree")
	_active.clear()

func _poll(handle: Request) -> void:
	if handle.done: return
	if handle._cancelled: _fail(handle, "cancelled", "Request cancelled"); return
	if handle._started == 0: return
	if Time.get_ticks_msec() - handle._started >= int(handle._operation.timeoutMS): _fail(handle, "timeout", "Request deadline exceeded", true); return
	var poll_error := handle._http.poll()
	var status := handle._http.get_status()
	if poll_error != OK or status in [HTTPClient.STATUS_CANT_RESOLVE, HTTPClient.STATUS_CANT_CONNECT, HTTPClient.STATUS_CONNECTION_ERROR, HTTPClient.STATUS_TLS_HANDSHAKE_ERROR]:
		_fail(handle, "transport_error", "HTTP connection failed"); return
	if not handle._sent:
		if status == HTTPClient.STATUS_CONNECTED:
			var send_error := handle._http.request(HTTPClient.METHOD_POST, _base_path + handle._operation.path, handle._request_headers, handle._body)
			if send_error != OK: _fail(handle, "transport_error", "Cannot send request"); return
			handle._sent = true
		return
	if not handle._headers and handle._http.has_response():
		handle._headers = true
		handle._status = handle._http.get_response_code()
		var content_type := ""
		for header in handle._http.get_response_headers():
			if header.to_lower().begins_with("content-type:"): content_type = header.get_slice(":", 1).get_slice(";", 0).strip_edges().to_lower()
		var expected := "text/event-stream" if handle._operation.transport == "http_sse" and handle._status >= 200 and handle._status < 300 else "application/json"
		if content_type != expected: _fail(handle, "unsupported_media_type", "Unexpected HTTP content type"); return
	if status == HTTPClient.STATUS_BODY:
		var chunk := handle._http.read_response_body_chunk()
		if not chunk.is_empty():
			if handle._operation.transport == "http_sse" and handle._status >= 200 and handle._status < 300:
				var parsed: Dictionary = handle._parser.feed(chunk)
				if not parsed.error.is_empty(): _fail(handle, parsed.error, "Invalid SSE data"); return
				for frame in parsed.frames:
					_accept(handle, frame)
					if handle.done: return
			else:
				if handle._buffer.size() + chunk.size() > max_response_bytes: _fail(handle, "response_too_large", "Response exceeds configured limit"); return
				handle._buffer.append_array(chunk)
		return
	if status in [HTTPClient.STATUS_CONNECTED, HTTPClient.STATUS_DISCONNECTED] and handle._headers:
		if handle._operation.transport == "http_sse" and handle._status >= 200 and handle._status < 300:
			_fail(handle, "incomplete_stream", "EOF before terminal event"); return
		_unary(handle)
	elif status == HTTPClient.STATUS_DISCONNECTED:
		_fail(handle, "transport_error", "Connection closed before response")

func _parse(bytes: PackedByteArray) -> Dictionary:
	var decoded: Dictionary = UTF8.decode(bytes)
	if decoded.has("error"): return {"error": "invalid_utf8"}
	var json := JSON.new()
	if json.parse(decoded.text) != OK or not json.data is Dictionary: return {"error": "invalid_json"}
	return {"body": json.data}

func _envelope(handle: Request, body: Dictionary) -> bool:
	if body.get("protocolVersion") != _contract.version: _fail(handle, "version_mismatch", "Response version differs"); return false
	if body.get("schemaHash") != _contract.schemaHash: _fail(handle, "schema_mismatch", "Response schema differs"); return false
	if body.get("requestId") != handle.request_id: _fail(handle, "invalid_frame", "Response request ID differs"); return false
	return true

func _unary(handle: Request) -> void:
	var parsed := _parse(handle._buffer)
	if parsed.has("error"): _fail(handle, parsed.error, "Invalid JSON response"); return
	var body: Dictionary = parsed.body
	if not _envelope(handle, body): return
	if body.has("error"):
		if body.has("data"): _fail(handle, "invalid_frame", "Response contains data and error"); return
		_remote_error(handle, body.error); return
	if handle._status < 200 or handle._status >= 300: _fail(handle, "http_error", "Unexpected HTTP status"); return
	if not body.has("data"): _fail(handle, "invalid_frame", "Response has no data"); return
	var validation: String = Wire.new(_contract.wireSchema).validate(handle._operation.responseType, body.data)
	if not validation.is_empty(): _fail(handle, "invalid_message", validation); return
	_finish(handle, body.data)

static func next_sequence(sequence: String) -> String:
	var next := sequence
	for index in range(next.length() - 1, -1, -1):
		if next[index] != "9":
			return next.substr(0, index) + str(int(next[index]) + 1) + next.substr(index + 1)
		next = next.substr(0, index) + "0" + next.substr(index + 1)
	return "1" + next

func _accept(handle: Request, frame: Dictionary) -> void:
	var parsed := _parse(frame.data.to_utf8_buffer())
	if parsed.has("error"): _fail(handle, parsed.error, "Invalid stream JSON"); return
	var body: Dictionary = parsed.body
	if not _envelope(handle, body): return
	var expected := next_sequence(handle._sequence)
	if body.get("sequence") != expected or frame.id != expected: _fail(handle, "invalid_sequence", "Stream sequence is not consecutive"); return
	handle._sequence = expected
	if frame.event == "protocol.error":
		if body.has("payload"): _fail(handle, "invalid_frame", "Error frame contains payload"); return
		_remote_error(handle, body.get("error")); return
	if not handle._operation.events.has(frame.event) or body.has("error"): _fail(handle, "invalid_event", "Unknown or conflicting stream event"); return
	var event: Dictionary = handle._operation.events[frame.event]
	if not body.get("payload") is Dictionary or body.payload.size() != 1 or not body.payload.has(event.field): _fail(handle, "invalid_event", "Event does not match response oneof"); return
	var validation: String = Wire.new(_contract.wireSchema).validate(handle._operation.responseType, body.payload)
	if not validation.is_empty(): _fail(handle, "invalid_message", validation); return
	var value := {"name": frame.event, "requestId": handle.request_id, "sequence": expected, "payload": body.payload}
	handle.event_received.emit(value)
	if event.terminal: _finish(handle, value)

func _remote_error(handle: Request, error: Variant) -> void:
	if not error is Dictionary or not error.get("code") is String or not error.get("message") is String or not error.get("retryable") is bool:
		_fail(handle, "invalid_frame", "Invalid transport error"); return
	_fail(handle, error.code, error.message, error.retryable)

func _finish(handle: Request, value: Variant) -> void:
	if handle.done: return
	handle.done = true; handle.result = value
	handle._http.close()
	handle.completed.emit(value)

func _fail(handle: Request, code: String, message: String, retryable: bool = false) -> void:
	if handle.done: return
	handle.done = true
	handle.error = {"code": code, "message": message, "retryable": retryable}
	handle._http.close()
	handle.failed.emit(handle.error)
