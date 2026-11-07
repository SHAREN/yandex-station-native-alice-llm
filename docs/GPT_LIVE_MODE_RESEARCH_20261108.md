# GPT-Live optional voice mode — initial engineering plan (issue #2 — Live-аудиорежим через VPS Renat)

**Status (2026-11-08):** research + a transport-free mode controller and tests.
**No production deployment, no audio gateway, no ASR hook, no credentials.**
Current native UniProxy TTS and search production binary remain unchanged.

## Why a separate mode

Our existing Quasar Bridge routes **text** to a model and returns streaming
**text** via the stock Yandex UniProxy TTS. Full duplex requires **continuous
bidirectional audio**, playback ownership, a session transport and voice
interruption; changing the LLM model or routing rule alone cannot provide it.

The first implementation must preserve the existing stock-first behaviour.
Music, timers, volume and other native commands must remain untouched when
Live is disabled; the existing single-session sentence TTS stays operational.

## Current code integration points (read-only audit)

- `internal/bridge/native.go` owns the UniProxy session state, `Client`,
  `OnVoiceMode`, `OnVoiceInput`, `OnASR`, `OnUserStop` and
  `ExternalUserStop`. The existing external TTS path already handles some
  stock stop commands; **do not overlay a second independent stop handler**.
- `internal/bridge/engine.go` and `routing.go` implement text-only
  `yandex/home/llm` decisions. A new Live mode is **not** an `llm` route:
  initiating Live must reserve/terminate the existing dialogue safely.
- `cmd/quasar-bridge/main.go` currently connects `OnASR` to a monitoring
  callback; this hook alone **cannot suppress** the normal stock answer.
  Before wiring any command, introduce an opt-in and tested consumption point
  for **final ASR only**, with no mutation when the feature is disabled.
- `internal/bridge/native_tts_stream.go` and related native TTS tests protect
  the accepted one-stream Yandex playback implementation. The Live audio
  playback path must be independently gated and must release ownership before
  returning to the stock player.
- `AGENTS.md` explicitly separates the Gemini Live/AP610 project:
  **this repository's scope is GPT-Live**, not Gemini Live. A shared gateway
  interface should remain provider-neutral, but Gemini integration belongs
  in the separate `SHAREN/yandex-station-gemini-bridge` project.

## First code delivered

`internal/live/control.go` is a pure Go control plane:

- **opt-in, disabled by default**; no side effects or network calls;
- exact, normalized Russian final-ASR commands for start and stop;
- transitions `idle → connecting → active → stopping → idle`;
- connecting/stop/inactivity deadlines with generation-scoped close signals;
- stale generation and concurrent-start protection;
- no interception of regular stock voice commands or idle `stop`.

`internal/live/control_test.go` tests normalization, negative phrases,
stock-mode isolation, concurrency, stale callbacks and all timeouts.

**Important:** `Expire` only returns the generation to cancel; the future
transport owner MUST cancel its socket, free the VQE tap/audio focus and
confirm release before routing stock audio again. This package is **not**
an audio implementation and is not called from production code yet.

## Next-stage gateway contract (not implemented)

1. Final ASR `Start` command detected safely; disable ordinary reply only
   after the Live backend accepts a connection. If connection fails,
   preserve native fail-open behaviour.
2. Station audio adapter uses stock processed VQE mic stream, not a second
   speech recognizer. Evaluate Alisa Voice's demonstrated Lite 1 tap:
   `YandexVqeC_GetOutputChannelData` type 6/index 0, 16 kHz.
3. Station↔VPS transport with authenticated short-lived session, encrypted
   connection, bounded queues and heartbeat; audio format agreed in PoC.
   Do not assume WebRTC RTP can be transparently forwarded over a WebSocket.
4. VPS↔GPT-Live adapter handles model session, token lifecycle, timeouts
   and interruption. Subscription-backed **unofficial** endpoints may fail
   or change; do not silently fall back to a different account or leak tokens
   to the station. Validate actual availability and terms before production.
5. The voice client controls playback/focus and half/full duplex detection.
   On close, stop, timeout, network error or failure, terminate all audio
   activity and return control to stock Alice. Repeated stop must be safe.
6. Default `disabled`, explicit feature flag and off-device rollback.
   No large or persistent backup on station. Respect
   `runbooks/yandex-stations/STATION_STORAGE_POLICY.md` before *any*
   station deployment; do not edit stock `maind`.

## Risks not yet resolved

- Concurrent tap/playback with our existing Quasar Bridge; a full integration
  test on the **physical Lite 1** is required.
- Alisa Voice Lite 1 compatibility tests cover one firmware; some users report
  immediate `goodbye(error)` even after refreshing ChatGPT auth:
  <https://github.com/rison1337/Alisa-ChatGPT-voice-assistant/issues/14>.
- WebRTC ICE/STUN and account authorization can fail independently of ordinary
  HTTPS checks. Need separate diagnostic codes, no guessed root cause.
- Review the licensing of Alisa Voice components before reusing its source;
  this first control-plane package was authored independently and copies
  **no Alisa Voice implementation**.

## Release gates

- `go test ./internal/live -count=1` and `go test ./... -count=1`;
- race test of concurrent control operations;
- hardware PoC proving VQE capture, no playback conflict, physical mic mute;
- session stop & interruption test while GPT is speaking;
- proven network/401/timeout recovery returning to the normal Alice;
- off-device SHA rollback, station storage check and approved production test.

**Do not enable Live on the actual speaker while only this control-plane
package exists.**
