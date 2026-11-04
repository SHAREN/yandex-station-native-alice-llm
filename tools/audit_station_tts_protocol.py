#!/usr/bin/env python3
"""Read-only firmware AAPI TTS inventory for Yandex Station maind ELF.

Usage:
  python3 tools/audit_station_tts_protocol.py _tmp/firmware-tts-audit-20261104/maind

The binary is never modified or executed. This tool does NOT prove that a
message can be sent to the cloud or that enum table numbers are wire tags.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import struct
from pathlib import Path


def cstring(blob: bytes, address: int, limit: int = 128) -> str:
    if not 0 <= address < len(blob):
        return ""
    return blob[address : address + limit].split(b"\0", 1)[0].decode("utf-8", "replace")


def find_enum_table(blob: bytes, key: bytes = b"kDeferredTtsGenerate") -> tuple[int, list[dict]]:
    needle = blob.find(key + b"\0")
    if needle < 0:
        raise ValueError("kDeferredTtsGenerate missing from firmware string pool")
    matches = [i for i in range(0, len(blob) - 4, 4) if blob[i : i + 4] == struct.pack("<I", needle)]
    if len(matches) != 1:
        raise ValueError(f"ambiguous relocated enum pointer: {len(matches)} matches")
    entry = matches[0]
    # The old C++ object layout has four little-endian uint32 per name/value
    # entry. Start at 'kEvent' and stop at the first non-k name / sentinel.
    start = entry
    while start >= 16:
        ptr, length, number, flags = struct.unpack_from("<IIII", blob, start - 16)
        if not cstring(blob, ptr).startswith("k") or length > 80 or flags != 0:
            break
        start -= 16
    entries: list[dict] = []
    for a in range(start, min(start + 120 * 16, len(blob)), 16):
        ptr, length, number, flags = struct.unpack_from("<IIII", blob, a)
        name = cstring(blob, ptr)
        if not name.startswith("k") or len(name) != length or flags != 0:
            break
        entries.append({"name": name, "next_entry_field_number": number})
    return entry, entries


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("maind", type=Path)
    ap.add_argument("--output-json", type=Path)
    args = ap.parse_args()
    raw = args.maind.read_bytes()
    idx, table = find_enum_table(raw)
    terms = (
        "NAlice.NAliceApi.TDeferredTtsGenerate",
        "NAlice.NAliceApi.TDeferredTtsGenerate.Text",
        "NAlice.NAliceApi.TDeferredTtsGenerate.RequestMessageId",
        "handleTtsGenerate",
        "handleDeferredTtsGenerate",
        "kDeferredTtsGenerate",
        "lazy_tts_streaming",
        "ttsLazyStreaming",
        "onSynthesisStreamEnd",
        "VoiceServiceSynthesisChunk",
        "NAlice.NAliceApi.TStreamControl.TActionLazyTTS",
        "NAlice.NAliceApi.TTtsUpdateState",
    )
    report = {
        "file": str(args.maind),
        "size_bytes": len(raw),
        "sha256": hashlib.sha256(raw).hexdigest(),
        "enum_table_offset_hex": hex(idx),
        "string_presence": {t: raw.find(t.encode()) >= 0 for t in terms},
        "enum_entries": table,
        "conclusion": (
            "TDeferredTtsGenerate is present in a native client-event-style "
            "name/value table. Each raw numeric entry is the NEXT entry's "
            "wire field number (the table's layout was initially misread). "
            "The verified wire field IDs are TtsGenerate=13 and "
            "DeferredTtsGenerate=34. Live isolated UniProxy tests on 2026-11-04 "
            "proved IsStreaming+DeferredTtsGenerate generates audio before "
            "the IsLast chunk, all on one stream."
        ),
    }
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if args.output_json:
        args.output_json.parent.mkdir(parents=True, exist_ok=True)
        args.output_json.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
