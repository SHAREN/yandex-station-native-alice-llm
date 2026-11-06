#!/usr/bin/env python3
"""Offline tests: fixtures contain no real user conversation data."""
import json
import unittest

from alice_journal_report import local_time, parse_jsonl, summarize


class JournalReportTests(unittest.TestCase):
    def test_go_nanosecond_timestamp(self):
        self.assertEqual(local_time("2026-11-06T13:20:40.208349216Z"),
                         "06.11.2026 18:20:40 (+05)")

    def test_group_progress_by_request_and_route(self):
        source = [
            {"time": "2026-11-06T13:20:40Z", "session": "s", "request": "a", "event": "request"},
            {"session": "s", "request": "a", "event": "transcript", "text": "Включи свет"},
            {"session": "s", "request": "a", "event": "progress", "text": "Го"},
            {"session": "s", "request": "a", "event": "progress", "text": "тово"},
            {"session": "s", "request": "a", "event": "route", "route": "llm"},
            {"session": "s", "request": "a", "event": "response", "delivered": True, "elapsed_ms": 510},
            {"time": "2026-11-06T13:21:40Z", "session": "s", "request": "b", "event": "request"},
            {"session": "s", "request": "b", "event": "transcript"},
            {"session": "s", "request": "b", "event": "route", "route": "yandex"},
        ]
        records, skipped = parse_jsonl(
            ("\n".join(json.dumps(row, ensure_ascii=False) for row in source)).encode("utf-8")
        )
        result = summarize(records, skipped)
        self.assertEqual(result["event_count"], 9)
        self.assertEqual(result["request_count"], 2)
        self.assertEqual(result["routes"], {"llm": 1, "yandex": 1})
        self.assertEqual(result["requests"][0]["reply"], "Готово")
        self.assertTrue(result["requests"][0]["delivered"])
        self.assertEqual(result["requests"][0]["elapsed_ms"], 510)
        self.assertIsNone(result["requests"][1]["utterance"])
        self.assertEqual(result["command_frequency"], [("включи свет", 1)])

    def test_invalid_lines_skipped_not_executed(self):
        records, skipped = parse_jsonl(b'{"event":"request","request":"a"}\nnot json\n{"foo": 2}\n')
        self.assertEqual(len(records), 1)
        self.assertEqual(skipped, 2)


if __name__ == "__main__":
    unittest.main()
