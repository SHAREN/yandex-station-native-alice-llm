#!/usr/bin/env python3
import json
import time
import uuid
import threading

import requests
import websocket
from requests.adapters import HTTPAdapter
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN_HOST = "0.0.0.0"
LISTEN_PORT = 8811
UPSTREAM = "http://100.68.233.33:8317/v1/responses"
UPSTREAM_WS = "ws://100.68.233.33:8317/v1/responses"
MODEL = "gpt-6.1-terra"
SERVICE_TIER = "priority"
ALLOWED = {"127.0.0.1", "::1", "192.168.0.119", "192.168.0.136", "192.168.0.22"}
INSTRUCTIONS = (
    "Ты голосовой ассистент. Отвечай по-русски, естественно и по существу. "
    "Не добавляй искусственных коротких вступлений, приветствий или фраз-заполнителей. "
    "Формулируй ответ обычным связным текстом, не подстраивая длину первого предложения под озвучивание. "
    "Не описывай внутренние рассуждения."
)

_lock = threading.Lock()
_last = {}
_session = requests.Session()
_adapter = HTTPAdapter(pool_connections=2, pool_maxsize=4, max_retries=0, pool_block=False)
_session.mount("http://", _adapter)
_session.mount("https://", _adapter)
_ws_lock = threading.Lock()
_ws_conn = None


def now_wall():
    return time.time()


def message_text(item):
    if not isinstance(item, dict):
        return ""
    content = item.get("content", "")
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts = []
        for part in content:
            if isinstance(part, dict):
                text = part.get("text") or part.get("content")
                if text:
                    parts.append(str(text))
        return "\n".join(parts).strip()
    return ""


def latest_user_text(messages):
    if not isinstance(messages, list):
        return ""
    for item in reversed(messages):
        if isinstance(item, dict) and item.get("role") == "user":
            text = message_text(item)
            if text:
                return text
    return ""


def responses_input(messages):
    if not isinstance(messages, list):
        return []
    result = []
    for item in messages:
        if not isinstance(item, dict):
            continue
        role = item.get("role")
        if role not in ("user", "assistant"):
            continue
        text = message_text(item)
        if not text:
            continue
        content_type = "input_text" if role == "user" else "output_text"
        result.append({"role": role, "content": [{"type": content_type, "text": text}]})
    return result


def stream_response_http(messages, text, request_id, on_delta=None):
    started_wall = now_wall()
    t0 = time.perf_counter()
    payload = {
        "model": MODEL,
        "instructions": INSTRUCTIONS,
        "input": responses_input(messages),
        "reasoning": {"effort": "none"},
        "service_tier": SERVICE_TIER,
        "stream": True,
        "max_output_tokens": 512,
        "store": False,
    }
    first_event = None
    first_token = None
    chunks = []
    event_count = 0
    status = None
    try:
        with _session.post(UPSTREAM, json=payload, stream=True, timeout=(5, 90)) as resp:
            resp.raise_for_status()
            status = resp.status_code
            for raw in resp.iter_lines(decode_unicode=False):
                line = raw.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if not data or data == "[DONE]":
                    continue
                try:
                    event = json.loads(data)
                except json.JSONDecodeError:
                    continue
                event_count += 1
                if first_event is None:
                    first_event = time.perf_counter()
                if event.get("type") == "response.output_text.delta":
                    delta = event.get("delta") or ""
                    if delta:
                        if first_token is None:
                            first_token = time.perf_counter()
                        chunks.append(delta)
                        if on_delta is not None:
                            on_delta(delta)
        done = time.perf_counter()
        answer = "".join(chunks).strip()
        timing = {
            "request_id": request_id,
            "model": MODEL,
            "input_text": text,
            "input_chars": len(text),
            "output_chars": len(answer),
            "upstream_status": status,
            "started_unix": started_wall,
            "first_event_ms": None if first_event is None else round((first_event - t0) * 1000, 1),
            "first_token_ms": None if first_token is None else round((first_token - t0) * 1000, 1),
            "first_token_unix": None if first_token is None else started_wall + (first_token - t0),
            "complete_ms": round((done - t0) * 1000, 1),
            "complete_unix": started_wall + (done - t0),
            "events": event_count,
            "ok": bool(answer),
        }
        with _lock:
            _last.clear()
            _last.update(timing)
        return answer, timing
    except Exception as exc:
        done = time.perf_counter()
        timing = {
            "request_id": request_id,
            "model": MODEL,
            "input_text": text,
            "input_chars": len(text),
            "started_unix": started_wall,
            "first_event_ms": None if first_event is None else round((first_event - t0) * 1000, 1),
            "first_token_ms": None if first_token is None else round((first_token - t0) * 1000, 1),
            "first_token_unix": None if first_token is None else started_wall + (first_token - t0),
            "complete_ms": round((done - t0) * 1000, 1),
            "complete_unix": started_wall + (done - t0),
            "events": event_count,
            "ok": False,
            "error": f"{type(exc).__name__}: {exc}"[:500],
        }
        with _lock:
            _last.clear()
            _last.update(timing)
        raise


def _close_ws():
    global _ws_conn
    if _ws_conn is not None:
        try:
            _ws_conn.close()
        except Exception:
            pass
        _ws_conn = None


def _get_ws():
    global _ws_conn
    if _ws_conn is None:
        _ws_conn = websocket.create_connection(UPSTREAM_WS, timeout=90, enable_multithread=True)
    return _ws_conn


def stream_response_ws(messages, text, request_id, on_delta=None):
    started_wall = now_wall()
    t0 = time.perf_counter()
    first_event = None
    first_token = None
    chunks = []
    event_count = 0
    reported_tier = None
    payload = {
        "type": "response.create",
        "model": MODEL,
        "instructions": INSTRUCTIONS,
        "input": responses_input(messages),
        "reasoning": {"effort": "none"},
        "service_tier": SERVICE_TIER,
        "max_output_tokens": 512,
        "store": False,
    }
    with _ws_lock:
        for attempt in range(2):
            try:
                ws = _get_ws()
                ws.send(json.dumps(payload, ensure_ascii=False))
                while True:
                    raw = ws.recv()
                    if not raw:
                        raise ConnectionError("upstream websocket closed")
                    event = json.loads(raw)
                    event_count += 1
                    if first_event is None:
                        first_event = time.perf_counter()
                    response = event.get("response") or {}
                    if isinstance(response, dict) and response.get("service_tier"):
                        reported_tier = response.get("service_tier")
                    if event.get("type") == "response.output_text.delta":
                        delta = event.get("delta") or ""
                        if delta:
                            if first_token is None:
                                first_token = time.perf_counter()
                            chunks.append(delta)
                            if on_delta is not None:
                                on_delta(delta)
                    event_type = event.get("type")
                    if event_type == "response.failed":
                        raise RuntimeError(f"upstream websocket response.failed: {event.get('response') or event}")
                    if event_type in ("response.completed", "response.incomplete"):
                        done = time.perf_counter()
                        answer = "".join(chunks).strip()
                        timing = {
                            "request_id": request_id,
                            "model": MODEL,
                            "input_text": text,
                            "input_chars": len(text),
                            "output_chars": len(answer),
                            "transport": "websocket",
                            "service_tier_requested": SERVICE_TIER,
                            "service_tier_reported": reported_tier,
                            "started_unix": started_wall,
                            "first_event_ms": None if first_event is None else round((first_event - t0) * 1000, 1),
                            "first_token_ms": None if first_token is None else round((first_token - t0) * 1000, 1),
                            "first_token_unix": None if first_token is None else started_wall + (first_token - t0),
                            "complete_ms": round((done - t0) * 1000, 1),
                            "complete_unix": started_wall + (done - t0),
                            "events": event_count,
                            "ok": bool(answer),
                        }
                        with _lock:
                            _last.clear()
                            _last.update(timing)
                        return answer, timing
            except Exception as exc:
                _close_ws()
                if chunks:
                    err = RuntimeError(f"websocket failed after partial output: {type(exc).__name__}: {exc}")
                    err.partial_output = True
                    raise err from exc
                if attempt == 0:
                    continue
                raise
    raise RuntimeError("websocket request ended unexpectedly")


def stream_response(messages, text, request_id, on_delta=None):
    try:
        return stream_response_ws(messages, text, request_id, on_delta=on_delta)
    except Exception as exc:
        if getattr(exc, "partial_output", False):
            raise
        print(f"WS fallback request={request_id}: {type(exc).__name__}: {exc}", flush=True)
        return stream_response_http(messages, text, request_id, on_delta=on_delta)


class Handler(BaseHTTPRequestHandler):
    server_version = "QuasarLLMRelay/0.1"

    def log_message(self, fmt, *args):
        print(time.strftime("%Y-%m-%d %H:%M:%S"), self.client_address[0], fmt % args, flush=True)

    def allowed(self):
        if self.client_address[0] in ALLOWED:
            return True
        self.send_error(403)
        return False

    def send_json(self, code, obj):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if not self.allowed():
            return
        if self.path == "/healthz":
            self.send_json(200, {"status": "ok", "model": MODEL, "upstream": UPSTREAM})
            return
        if self.path == "/timing":
            with _lock:
                data = dict(_last)
            self.send_json(200, data)
            return
        self.send_error(404)

    def do_POST(self):
        if not self.allowed():
            return
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 256 * 1024:
                raise ValueError("invalid request size")
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
            messages = payload.get("messages")
            text = latest_user_text(messages)
            if not text:
                raise ValueError("no user text")
            request_id = uuid.uuid4().hex[:12]
            if payload.get("stream") is True:
                created = int(time.time())
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream; charset=utf-8")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                self.end_headers()

                def emit(delta=None, finish_reason=None):
                    chunk = {
                        "id": f"chatcmpl-{request_id}",
                        "object": "chat.completion.chunk",
                        "created": created,
                        "model": MODEL,
                        "choices": [{
                            "index": 0,
                            "delta": {} if delta is None else {"content": delta},
                            "finish_reason": finish_reason,
                        }],
                    }
                    self.wfile.write(b"data: " + json.dumps(chunk, ensure_ascii=False).encode("utf-8") + b"\n\n")
                    self.wfile.flush()

                try:
                    emit()
                    answer, timing = stream_response(messages, text, request_id, on_delta=lambda delta: emit(delta=delta))
                    emit(finish_reason="stop")
                    self.wfile.write(b"data: [DONE]\n\n")
                    self.wfile.flush()
                except Exception as exc:
                    print(f"stream request={request_id} failed: {type(exc).__name__}: {exc}", flush=True)
                    return
                print(
                    f"LLM request={request_id} first_token_ms={timing.get('first_token_ms')} "
                    f"complete_ms={timing.get('complete_ms')} input={text!r} stream=true",
                    flush=True,
                )
                return

            answer, timing = stream_response(messages, text, request_id)
            print(
                f"LLM request={request_id} first_token_ms={timing.get('first_token_ms')} "
                f"complete_ms={timing.get('complete_ms')} input={text!r}",
                flush=True,
            )
            self.send_json(200, {
                "id": f"chatcmpl-{request_id}",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": MODEL,
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": answer},
                    "finish_reason": "stop",
                }],
                "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
            })
        except ValueError as exc:
            self.send_json(400, {"error": {"message": str(exc)}})
        except requests.HTTPError as exc:
            response = exc.response
            code = None if response is None else response.status_code
            detail = "" if response is None else response.text[:1000]
            self.send_json(502, {"error": {"message": f"upstream HTTP {code}: {detail}"}})
        except Exception as exc:
            self.send_json(502, {"error": {"message": f"{type(exc).__name__}: {exc}"[:1000]}})


if __name__ == "__main__":
    print(f"Quasar LLM relay listening on {LISTEN_HOST}:{LISTEN_PORT}; model={MODEL}", flush=True)
    ThreadingHTTPServer((LISTEN_HOST, LISTEN_PORT), Handler).serve_forever()
