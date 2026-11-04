# Telemetry enabled for stock Alice TTS A/B — 2026-11-04

## Live state before the user's listening test

- Physical Yandex Station Lite 1, stock maind SHA256: 2dad0ffc61ba73d15ea813b754d7a56866d0477c321dcad9138b8d06e14ddbf1 (firmware untouched).
- Home Assistant `input_select.komnata_assistent` switched from `Quasar Assistant` to **`Алиса`**, state confirmed at 2026-11-04 21:32:39 +05. User has not yet spoken the target long-story prompt.
- Bridge with metadata-only trace compiled and deployed to `/bkupgrade/data/quazar/betteryam/data/plugins/quasar-bridge/bin/quasar-bridge`: SHA256 `13ffa6b27771b9c2850fc2c742ff12958ac4ab3908907a0cff8ebd0168299799`.
- Previous Bridge 0.4.5f SHA256 `2a45c6cffc7e4469bd4b1d68f597c2f9dafa759f707763e8dae9de681b634206` backed up on Home PC `_tmp/quasar-bridge-0.4.5f-rollback`; compressed device backup `/tmp/quasar-bridge-0.4.5f.gz` (tmpfs only; lost on reboot).
- Trace enabled by sentinel `/data/opt/quasar-bridge/enable-stock-tts-trace`. Output is `/data/opt/quasar-bridge/stock-tts-telemetry.jsonl`, mode 0600. The trace retains NO actual microphone or playback audio, transcripts, model response text or authentication credentials.
- Bridge health status `ok`; stock maind local connection to 127.0.0.1:8443 **ESTABLISHED**, frame counters advancing, SHA validated after deployment.

## What is captured

Metadata for client-side `VoiceInput`, `TtsGenerate`, `DeferredTtsGenerate`, `StreamControl.Close`; server-side `AliceResponse`, `DeferredAliceResponse`, `TtsSpeak` (protobuf or JSON), `lazy_tts_streaming`, `SoundChunkType`, `SubStreamId`, first Opus audio receipt, Ogg page BOS/EOS/serial/sequence, network stream ID, timestamp and pseudonymized request references.

A separate controlled synthetic UniProxy TTS probe completed successfully **before** selecting stock Alice. It recorded exactly: `TtsGenerate(IsStreaming=true)`, nonfinal `DeferredTtsGenerate`, `TtsSpeakJSON(lazy_tts_streaming=false)`, first audio, Ogg BOS+EOS and final `DeferredTtsGenerate(IsLast=true)`, then `StreamControlJSON`. Trace file is verified. Probe did not play audio on Station and has no user dialogue. The trace from 2026-11-04 16:31 UTC predates the stock mode switch.

## Next steps

User physically says one long natural prompt, e.g. “Алиса, расскажи мне длинную историю на две-три минуты с несколькими событиями, без перечислений.” User then confirms in this conversation that the test is complete. **Only then** analyze the new telemetry since the confirmed selector switch (UTC 2026-11-04 16:32:39), look for genuine upstream TTS (cloud) versus locally synthesized, compare text streaming and Ogg boundaries. After analysis switch back to `Quasar Assistant` (unless user requests otherwise) and verify the state and health.

Do not confuse `Bridge mode=hybrid` (its configured routing capability) with the per-request Home Assistant voice selection `Алиса`. When strict stock selection is active, `native_voice_stock_passthrough` is used.

## Cautions

- The trace's Ogg analysis is keyed by network `stream_id` within the running Bridge and is **not per websocket connection**, so the same stream ID can reoccur across isolated WebSocket sessions. For the actual user test, filter by timestamp and Ogg serial. If several simultaneous test WebSockets occur, extend the key with the connection scope before drawing conclusions.
- The trace feature sentinel is checked once when the Bridge starts; remove sentinel and restart Bridge to disable. Saving this trace is temporary; after study, disable it to avoid accumulation (hard cap 16 MiB, private mode 0600).
- Previous 0.4.5f rollback is safe but requires stopping Bridge and restoring the original file from Home PC (or decompressing the temporary on-device rollback file) followed by normal init start. The deployed trace does not alter firmware or TTS algorithms.

Reference: `docs/STOCK_ALICE_STREAMING_PROOF_PLAN_20261104.md` and `docs/NATIVE_TTS_PROSODY_INVESTIGATION_20261104.md`.
