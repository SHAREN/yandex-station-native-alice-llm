#!/usr/bin/env python3
"""Read-only, local report for stock Alice / Quasar Bridge conversation JSONL.

No background service, audio capture, HTTP uploads, or modification to the speaker.
Without --stdin/--file this uses the already-installed Windows alice-room helper.
"""
from __future__ import annotations

import argparse
from collections import Counter, OrderedDict
from datetime import datetime, timedelta, timezone
import json
import re
import statistics
from pathlib import Path
import subprocess
import sys

STATION_BASE = "/data/opt/quasar-bridge/runtime/conversations.jsonl"
LOCAL_TZ = timezone(timedelta(hours=5))
MAX_BYTES = 15 * 1024 * 1024
MAX_TEXT = 6000


def fetch_windows_helper() -> bytes:
    if sys.platform != "win32":
        raise RuntimeError(
            "Auto-fetch requires native Windows Python. For WSL pipe: "
            "bash ./alice-room 'cat /data/opt/quasar-bridge/runtime/conversations.jsonl' "
            "| python3 yandex-station-native-alice-llm/tools/alice_journal_report.py --stdin"
        )
    helper = Path.home() / ".agents" / "skills" / "alice-room" / "scripts" / "alice_room.py"
    if not helper.is_file():
        raise RuntimeError(f"Installed alice-room helper missing: {helper}")
    # Read old backups before the current file; no shell interpolation of user input.
    command = (
        "for f in " + " ".join(
            (STATION_BASE + ".2", STATION_BASE + ".1", STATION_BASE)
        ) + '; do if [ -f "$f" ]; then cat "$f"; fi; done'
    )
    proc = subprocess.run([sys.executable, str(helper), command],
                          capture_output=True, timeout=90, check=False)
    if proc.returncode != 0:
        note = proc.stderr.decode("utf-8", "replace").strip()[-700:]
        raise RuntimeError(f"Station helper failed (exit={proc.returncode}): {note}")
    return proc.stdout


def parse_jsonl(data: bytes) -> tuple[list[dict], int]:
    if len(data) > MAX_BYTES:
        raise ValueError("Journal too large for single read-only report")
    records = []
    skipped = 0
    for raw in data.decode("utf-8-sig", "replace").splitlines():
        if not raw.strip():
            continue
        try:
            entry = json.loads(raw)
            if not isinstance(entry, dict) or not isinstance(entry.get("event"), str):
                skipped += 1
                continue
            records.append(entry)
        except (json.JSONDecodeError, ValueError):
            skipped += 1
    return records, skipped


def summarize(records: list[dict], skipped: int = 0) -> dict:
    requests = OrderedDict()
    events = Counter()
    for item in records:
        kind = item["event"]
        events[kind] += 1
        request_id = item.get("request")
        if not request_id:
            continue
        key = (item.get("session"), request_id)
        if key not in requests:
            requests[key] = {
                "time": item.get("time", ""), "request_id": request_id,
                "route": None, "utterance": None, "reply": "",
                "elapsed_ms": None, "events": [], "delivered": False,
                "truncated": False,
            }
        v = requests[key]
        v["events"].append(kind)
        if kind == "transcript" and isinstance(item.get("text"), str):
            v["utterance"] = item["text"].strip()
        if kind == "progress" and isinstance(item.get("text"), str):
            v["reply"] += item["text"]
        elif kind == "response" and not v["reply"] and isinstance(item.get("text"), str):
            v["reply"] = item["text"]
        if isinstance(item.get("route"), str) and item["route"]:
            v["route"] = item["route"]
        if kind == "response":
            v["elapsed_ms"] = item.get("elapsed_ms")
            v["delivered"] = item.get("delivered") is True
        if item.get("truncated"):
            v["truncated"] = True
    by_route = Counter(v["route"] or "unknown" for v in requests.values())
    phrases = Counter(v["utterance"].casefold().strip(" .,!?") for v in requests.values()
                      if v["utterance"])
    request_values = list(requests.values())
    elapsed = [v["elapsed_ms"] for v in request_values
               if v["route"] == "llm" and isinstance(v["elapsed_ms"], (int, float))]
    quality = {
        "asr_text_present": sum(bool(v["utterance"]) for v in request_values),
        "asr_text_missing": sum(not v["utterance"] for v in request_values),
        "asr_empty_event": sum("transcript" in v["events"] and not v["utterance"] for v in request_values),
        "asr_no_event": sum("transcript" not in v["events"] for v in request_values),
        "recorded_responses_delivered": sum(v["delivered"] for v in request_values),
        "responses_not_in_journal": sum("response" not in v["events"] for v in request_values),
        "cancel_events": events["cancel"],
        "truncated_requests": sum(v["truncated"] for v in request_values),
        "llm_response_elapsed_ms": {
            "samples": len(elapsed),
            "mean": round(statistics.mean(elapsed)) if elapsed else None,
            "median": round(statistics.median(elapsed)) if elapsed else None,
            "minimum": min(elapsed) if elapsed else None,
            "maximum": max(elapsed) if elapsed else None,
        },
    }
    return {
        "event_count": len(records), "skipped_lines": skipped,
        "request_count": len(request_values), "events_by_type": dict(events),
        "routes": dict(by_route), "command_frequency": phrases.most_common(),
        "quality": quality, "requests": request_values,
    }


def local_time(timestamp: str) -> str:
    if not timestamp:
        return "?"
    try:
        # Go logs nanoseconds; Windows Python installations may parse only 6 digits.
        iso = re.sub(r"(\.\d{6})\d+(?=(?:Z|[+-]\d\d:\d\d)$)", r"\1", timestamp)
        dt = datetime.fromisoformat(iso.replace("Z", "+00:00"))
        return dt.astimezone(LOCAL_TZ).strftime("%d.%m.%Y %H:%M:%S (+05)")
    except ValueError:
        return timestamp


def render(result: dict, last: int, no_text: bool) -> str:
    lines = [
        "ЖУРНАЛ АЛИСЫ — только чтение, локально",
        f"Событий: {result['event_count']}; голосовых запросов: {result['request_count']}; "
        f"некорректных строк: {result['skipped_lines']}",
        "Маршруты: " + ", ".join(f"{k}={v}" for k, v in result["routes"].items()),
        "Типы событий: " + ", ".join(f"{k}={v}" for k, v in result["events_by_type"].items()),
    ]
    q = result["quality"]
    timing = q["llm_response_elapsed_ms"]
    lines.append(
        f"Полнота: текст ASR {q['asr_text_present']}/{result['request_count']}; "
        f"без текста {q['asr_text_missing']} (пустое ASR-событие "
        f"{q['asr_empty_event']}, нет ASR-события {q['asr_no_event']}); "
        f"ответов с delivered=true "
        f"{q['recorded_responses_delivered']}; ответов нет в журнале "
        f"{q['responses_not_in_journal']}; cancel-событий {q['cancel_events']}"
    )
    if timing["samples"]:
        lines.append(f"Время до записанного LLM-ответа: среднее {timing['mean']} мс, "
                     f"медиана {timing['median']} мс, минимум {timing['minimum']} мс, "
                     f"максимум {timing['maximum']} мс (n={timing['samples']}).")
    lines.append("Частые фразы (по запросам):")
    for phrase, count in result["command_frequency"][:10]:
        if phrase:
            lines.append(f"  {count} × {phrase if not no_text else '[текст скрыт]'}")
    lines.append(f"Последние запросы: {min(last, len(result['requests']))}")
    for v in result["requests"][-last:]:
        lines.append(f"• {local_time(v['time'])}, маршрут={v['route'] or 'нет'}, "
                     f"ответ={v['elapsed_ms'] if v['elapsed_ms'] is not None else '?'} мс, "
                     f"delivered={v['delivered']}, события={','.join(v['events'])}")
        if not no_text:
            lines.append(f"  Пользователь: {v['utterance'] or '[текст ASR не поступил]'}")
            if v["reply"]:
                lines.append("  Нейросеть: " + v["reply"][:MAX_TEXT] +
                             ("… [обрезано для отчёта]" if len(v["reply"]) > MAX_TEXT else ""))
            else:
                lines.append("  Ответ: [отсутствует в JSONL; для маршрута yandex это ожидаемо]")
    lines.append("Примечание: delivered=true подтверждает передачу в WebSocket, не физическое воспроизведение.")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    # Keep Russian dialogue text intact in native Windows CMD/PowerShell redirection.
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--file", type=Path, help="Analyze existing local JSONL; no network")
    source.add_argument("--stdin", action="store_true", help="Read JSONL from stdin; no network")
    parser.add_argument("--last", type=int, default=10, help="Last N voice requests (default: 10)")
    parser.add_argument("--no-text", action="store_true", help="Print only metadata/counts")
    parser.add_argument("--json", action="store_true", help="Machine-readable summary for other agents")
    opts = parser.parse_args(argv)
    if not 1 <= opts.last <= 1000:
        parser.error("--last must be between 1 and 1000")
    try:
        if opts.file:
            data = opts.file.read_bytes()
        elif opts.stdin:
            data = sys.stdin.buffer.read(MAX_BYTES + 1)
        else:
            data = fetch_windows_helper()
        records, skipped = parse_jsonl(data)
        result = summarize(records, skipped)
        if opts.json:
            selected = dict(result)
            selected["requests"] = result["requests"][-opts.last:]
            if opts.no_text:
                selected["command_frequency"] = []
                selected["requests"] = [{k: v for k, v in rec.items()
                                         if k not in ("utterance", "reply")}
                                        for rec in selected["requests"]]
            print(json.dumps(selected, ensure_ascii=False, indent=2))
        else:
            print(render(result, opts.last, opts.no_text))
        return 0
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as exc:
        print(f"alice_journal_report: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
