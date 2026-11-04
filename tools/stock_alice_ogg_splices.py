#!/usr/bin/env python3
"""Exact Ogg/Opus logical-bitstream join analysis from a captured stock Alice reply.

Input is server-output audio only (never user microphone). The output is a
sample-accurate splice timeline, plus one independently decodable Ogg file
per logical stream. Text from the private separate capture can later be
aligned to segment speech. Network arrival timestamps are NOT playback times.
"""
import argparse
import json
import struct
import subprocess
from pathlib import Path


def parse_ogg(blob: bytes):
    at = 0
    sequences = []
    by_serial = {}
    while at < len(blob):
        if len(blob) - at < 27 or blob[at:at+4] != b"OggS":
            raise ValueError(f"Not an Ogg page at input byte {at}")
        if blob[at+4] != 0:
            raise ValueError(f"Unexpected Ogg version at byte {at}")
        n_segments = blob[at+26]
        header_end = at + 27 + n_segments
        if header_end > len(blob):
            raise ValueError("Truncated Ogg segment table")
        page_end = header_end + sum(blob[at+27:header_end])
        if page_end > len(blob):
            raise ValueError("Truncated Ogg page")
        page = blob[at:page_end]
        body = blob[header_end:page_end]
        flag = blob[at+5]
        granule, serial, sequence = struct.unpack_from("<QII", blob, at+6)
        if flag & 2:
            if serial in by_serial:
                raise ValueError(f"Duplicate Ogg serial {serial}; reset session to analyze independently")
            part = {"serial":serial, "pages":[], "bos":0, "eos":0, "pre_skip":None,
                    "end_granule":None, "first_page_offset":at, "last_page_offset":None,
                    "first_opus_head":False}
            by_serial[serial] = part
            sequences.append(part)
        elif serial not in by_serial:
            raise ValueError(f"Missing Ogg BOS for serial {serial} at byte {at}")
        part = by_serial[serial]
        part["pages"].append(page)
        if flag & 2:
            part["bos"] += 1
            if body.startswith(b"OpusHead") and len(body) >= 12:
                part["first_opus_head"] = True
                part["pre_skip"] = struct.unpack_from("<H", body, 10)[0]
        if flag & 4:
            part["eos"] += 1
            part["end_granule"] = granule
            part["last_page_offset"] = page_end
        at = page_end
    if not sequences:
        raise ValueError("No Ogg logical streams in file")
    return sequences


def main():
    p = argparse.ArgumentParser()
    p.add_argument("source", type=Path)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument("--decode", action="store_true", help="Decode each Ogg part to 16 kHz mono WAV with ffmpeg")
    p.add_argument("--text", type=Path, help="Optional private JSONL stock Alice response text capture")
    args = p.parse_args()
    data = args.source.read_bytes()
    parts = parse_ogg(data)
    args.out.mkdir(parents=True, exist_ok=True)
    story = []
    cumulative = 0
    for idx, part in enumerate(parts, 1):
        if part["bos"] != 1 or part["eos"] != 1 or part["pre_skip"] is None:
            raise ValueError(f"Logical stream {idx} incomplete or not Opus: {part['serial']}")
        samples = part["end_granule"] - part["pre_skip"]
        if samples <= 0:
            raise ValueError(f"Invalid duration for serial {part['serial']}")
        duration_ms = samples * 1000 / 48000
        path = args.out / f"segment_{idx:02d}.ogg"
        path.write_bytes(b"".join(part["pages"]))
        path.chmod(0o600)
        if args.decode:
            subprocess.run(["ffmpeg","-nostdin","-loglevel","error","-y","-i",str(path),
                            "-ac","1","-ar","16000",str(args.out / f"segment_{idx:02d}.wav")],
                           check=True)
        record = {
            "segment":idx,
            "serial":part["serial"],
            "pages":len(part["pages"]),
            "start_ms":round(cumulative,3),
            "duration_ms":round(duration_ms,3),
            "end_ms":round(cumulative+duration_ms,3),
            "end_sample_48k":part["end_granule"],
            "pre_skip_samples":part["pre_skip"],
            "ogg":str(path)
        }
        story.append(record)
        cumulative += duration_ms
    summary={"source":str(args.source),"total_audio_ms":round(cumulative,3),
             "segment_count":len(story),"splice_count":len(story)-1,
             "splices_at_ms":[s["end_ms"] for s in story[:-1]],
             "segments":story}
    if args.text is not None and args.text.is_file():
        response_parts=[json.loads(x) for x in args.text.read_text("utf-8").splitlines() if x.strip()]
        summary["response_text_fragments"]=response_parts
        summary["warning"]="Text deltas and audio logical streams require word alignment before claiming exact words at boundaries"
    output=args.out / "splice_timeline.json"
    output.write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding="utf-8")
    output.chmod(0o600)
    print(json.dumps({"segment_count":len(story),"splice_count":len(story)-1,
                      "splices_at_ms":summary["splices_at_ms"],
                      "total_audio_ms":summary["total_audio_ms"],"analysis":str(output)},
                     ensure_ascii=False,indent=2))


if __name__=="__main__":
    main()
