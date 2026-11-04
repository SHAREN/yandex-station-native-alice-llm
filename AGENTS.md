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

## Documentation
Update docs/CURRENT_STATE.md for the actual installed state, docs/ARCHITECTURE.md for system changes, and docs/REPOSITORY_SEPARATION.md if ownership or remotes change.
