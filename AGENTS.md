# Agent instructions — Native Alice LLM

## Scope and isolation
- This repository is the **canonical** workspace for the rooted Yandex Station Lite 1 + stock Alice stack + custom LLM, NOT Gemini Live.
- Implement the native UniProxy AAPI bridge, sequential-text/one-session streaming TTS, local LLM relay, safe production deployment, tests and diagnostics.
- Do not add Gemini Live full-duplex audio, its websocket proxy, live-agent server, AP610 microphone stack or their research history. The separate legacy project is SHAREN/yandex-station-gemini-bridge.
- This is a modified Quasar Bridge, not an original Yandex firmware rewrite. Preserve upstream notices and licensing.

## Stock-first rule
Before adding a feature inspect the physical firmware/protocol for existing implementations. Prefer native wake word, VQE/AEC, ASR, Opus playback, volume/LEDs and existing UniProxy TTS. Do not duplicate a native function unless its inadequacy is established.

## Development safety
- Inspect current status and logs before device modification. Every deployment needs verified old binary, rollback plan, SHA guard, health check, and ability to disable feature by sentinel.
- Production native streaming TTS: initial TtsGenerate with IsStreaming field 12; DeferredTtsGenerate client event field 34 with RequestMessageId, Text, IsLast, ChunkNum. Keep one audio stream. Do not confuse with Yandex Cloud SpeechKit v3 or Gemini Live.
- Preserve active voice commands routed to stock Alice, and stopping/interruption.
- Never commit private credentials, unredacted config, firmware dumps, recordings, binaries, personal logs or secrets. Keep _tmp outside git.
- Reproduce findings in tests and docs. Distinguish network protocol proof from physical audible acceptance.
- Windows/WSL: use tools/go-cross-build.sh for ARM64 Linux build. Go module is under src/quasar-bridge.
- Never create/start/resume/steer Codex Threads without explicit authorization from the user in the current message.

## Station storage hard gate (owner policy, 07.11.2026)

Before **every** station write, release, rollback or large staging, read local Home PC `D:\codexpro_workspace\runbooks\yandex-stations\STATION_STORAGE_POLICY.md` and run `runbooks/yandex-stations/check-room-storage.cmd --json` or WSL `./ps-file runbooks/yandex-stations/check-room-storage.ps1 -Json`. Stage new binaries **only in /tmp** temporarily; verify SHA and save current release/rollback **off-speaker** to Home PC/Home Server first. No retained `*.bak`, `.before`, rollback tarballs, duplicated binary or archives on persistent `/data` (`/bkupgrade`) or `/tmp`. Target post-change >=25 MiB UBI free; WARN below 25 MiB or >=75% used, CRIT below 15 MiB or >=85% used. Do not continue deployment if the storage preflight fails; do not auto-delete unclassified files. The tiny `endpoint-backup.json` is an ACTIVE stock fail-open dependency referenced by S98zzbetteryam, not an archival backup: keep it. As of 07.11.2026, 33 old backups moved to private Home PC archive and verified, /bkupgrade 55% used (~37.6 MiB free). Any release script requiring a former /tmp rollback must obtain and verify the external copy first.

## Conversation journal for agents
- To inspect real dialogue history, use the **read-only** `tools/alice-journal.cmd --last 20` on native Windows; add `--json` to pass structured results to another agent. Script: `tools/alice_journal_report.py`; on WSL it supports `--stdin` from the existing `alice-room` helper. Never publish raw conversations in Git.
- Existing speaker-side ring buffer is ~3 MB (1 MB current + 2 backups), **not a guaranteed 30-day archive**. Fastcommands without wake-word are not yet part of the Quasar JSONL; never infer their frequency from that file alone.
- Fast home intent research is documented in `docs/FAST_HOME_INTENTS_RESEARCH_20261106.md`; the real private Home Assistant entity mapping is in local Home PC `runbooks/yandex-stations/LOCAL_FAST_HOME_INTENTS_20261106.md`, not this public repo. Do not deploy new command routes without tests and a separate production approval.

## Documentation
Update docs/CURRENT_STATE.md for the actual installed state, docs/ARCHITECTURE.md for system changes, and docs/REPOSITORY_SEPARATION.md if ownership or remotes change.
