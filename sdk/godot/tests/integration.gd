extends SceneTree

const Client = preload("../protocol_client.gd")
var failures := 0

func check(condition: bool, description: String) -> void:
	if not condition:
		failures += 1
		printerr(description)

func _initialize() -> void:
	_run.call_deferred()

func wait_for(handle: RefCounted) -> void:
	var deadline := Time.get_ticks_msec() + 15000
	while not handle.done and Time.get_ticks_msec() < deadline:
		await process_frame
	check(handle.done, "Request did not finish")

func _run() -> void:
	var args := OS.get_cmdline_user_args()
	if args.size() != 2:
		printerr("Expected URL and generated protocol.gd path"); quit(1); return
	var generated: GDScript = load(args[1])
	if generated == null:
		printerr("Cannot load generated contract"); quit(1); return
	var client := Client.new()
	root.add_child(client)
	check(client.configure(args[0], generated.CONTRACT) == OK, "Configure")
	var data := {"prompt": "你好😀", "conversationId": "18446744073709551615"}
	var unary: RefCounted = client.request("chatComplete", data)
	await wait_for(unary)
	check(unary.error.is_empty(), "Unary: " + JSON.stringify(unary.error))
	if unary.error.is_empty():
		check(unary.result.text == "你好😀", "Unicode response")
		check(unary.result.usage.outputTokens == "18446744073709551615", "uint64 precision")
	var events: Array = []
	var streamed: RefCounted = client.stream("chatStream", data)
	streamed.event_received.connect(func(event: Dictionary) -> void: events.append(event))
	await wait_for(streamed)
	check(streamed.error.is_empty(), "SSE: " + JSON.stringify(streamed.error))
	check(events.size() == 2, "SSE event count")
	if events.size() == 2:
		check(events[0].payload.delta.text == "你好😀", "SSE Unicode")
		check(events[1].name == "completed" and events[1].sequence == "2", "SSE terminal")
	var invalid: RefCounted = client.request("chatComplete", {"conversationId": 123})
	await wait_for(invalid)
	check(invalid.error.get("code") == "invalid_message", "Reject wrong request type")
	var cancelled: RefCounted = client.stream("chatStream", data)
	cancelled.cancel()
	await wait_for(cancelled)
	check(cancelled.error.get("code") == "cancelled", "Cancellation")
	var truncated: RefCounted = client.stream("chatStream", {"prompt": "truncated"})
	await wait_for(truncated)
	check(truncated.error.get("code") == "incomplete_stream", "Stream truncation")
	var live_cancel: RefCounted = client.stream("chatStream", {"prompt": "cancel"})
	live_cancel.event_received.connect(func(_event: Dictionary) -> void: live_cancel.cancel())
	await wait_for(live_cancel)
	check(live_cancel.error.get("code") == "cancelled", "Cancel after event")
	var short_contract: Dictionary = generated.CONTRACT.duplicate(true)
	short_contract.operations.chatStream.timeoutMS = 20
	check(client.configure(args[0], short_contract) == OK, "Timeout configure")
	var timeout: RefCounted = client.stream("chatStream", {"prompt": "timeout"})
	await wait_for(timeout)
	check(timeout.error.get("code") == "timeout", "Timeout")
	var mismatch: Dictionary = generated.CONTRACT.duplicate(true)
	mismatch.schemaHash = "stale"
	check(client.configure(args[0], mismatch) == OK, "Reconfigure")
	var stale: RefCounted = client.request("chatComplete", data)
	await wait_for(stale)
	check(stale.error.get("code") == "schema_mismatch", "Reject stale schema")
	client.queue_free()
	await process_frame
	print("Godot Go HTTP/SSE integration: %d failures" % failures)
	quit(0 if failures == 0 else 1)
