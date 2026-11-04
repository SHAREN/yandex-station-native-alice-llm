# One-session native Yandex TTS cutover — 2026-11-04

## Production status

**Installed and feature enabled on physical Station Lite 1** via Bridge 0.4.5e. Deployed binary: `/bkupgrade/data/quazar/betteryam/data/plugins/quasar-bridge/bin/quasar-bridge` SHA256 `cbe8a72f0ba77385308c1e0922c289e5654961759f44a8b26250c61c3a338067`; process PID **5885** at deployment. Boot script is unchanged and the firmware root filesystem remains read-only. Health endpoint returned `status:ok`; `-check-backends` returned `LLM: ok`; shortly after restart, `client_frames=2` and `server_frames=2`, and netstat showed an established 127.0.0.1:8443 socket. HA `input_select.komnata_assistent` was read directly from Home Assistant and is **Quasar Assistant**.

Opt-in sentinel is present and persistent: `/data/opt/quasar-bridge/enable-native-streaming-tts`. Only the custom Quasar LLM streamed reply path uses this; native Yandex commands like `Тише` still follow their original route.

**Physical spoken acceptance remains outstanding.** Protobuf/integration tests and isolated UniProxy protocol tests establish correct transport; no one has listened to a complete story on the newly deployed 0.4.5e as part of this rollout.

## Implementation

- `src/quasar-bridge/internal/bridge/native_tts_stream.go`: one-session producer/consumer. The first complete natural sentence or clause triggers a single `TTtsGenerate(Text=...,IsStreaming=true)`. Subsequent completed chunks send `TDeferredTtsGenerate(RequestMessageId=initialTtsMessageID,Text=...,ChunkNum=1..n,IsLast=false)` **without waiting for the preceding audio source to close**. Upon LLM completion, an `IsLast=true` final chunk finishes the TTS session; empty final text is valid, confirmed against the live UniProxy. All messages use the stock AAPI and the Station's existing authentication and audio pipeline.
- The currently pinned public generated Go protobuf does not expose `TDeferredTtsGenerate` or `IsStreaming`: bridge uses `google.golang.org/protobuf/encoding/protowire` to append exactly documented, live-validated unknown fields: client event 34 for deferred; initial `TTtsGenerate.IsStreaming` field 12. Deferred text fields 1,2,3,4, respectively RequestMessageId, Text, IsLast, ChunkNum. Normal `TTtsGenerate` event 13 is unchanged.
- `native.go`: `p.nativeTextStream` tracks new requests. One `TtsSpeak` and one native output source are retained; repeated directives for the same stream cannot restart the Ogg decoder. One final upstream `StreamControl.Close` becomes one downstream end-of-stream. Old sequential/pre-fetch branch remains unchanged when opt-in sentinel is absent.
- `stream_sentences.go`: selects native one-session transport only when the opt-in sentinel exists (or explicitly toggled in unit tests). Natural punctuation boundaries still serve as *submission points*, not separate synthesized TTS sessions. For the first long sentence use a logical comma; subsequent chunks prioritize full sentence ends.
- `native_tts_stream_test.go`: confirms first audio request leaves before the LLM finishes; subsequent deferred messages share the initial TTS ID and increasing chunk numbers; empty IsLast signals completion; no additional TtsGenerate and only one TtsSpeak/close. Focused test repeated 20 times; full `go test ./internal/bridge ./cmd/quasar-bridge -count=1 -timeout=70s` passed.

## Known limitations / follow-up after speaking test

- Human-perceived prosody across text chunks is not yet tested. A single streaming server session is confirmed, but perfect intonation cannot be guaranteed from protocol tests.
- Complete spoken story and STOP/volume interruption should be verified on physical hardware; capture only timing/length metadata in `/data/opt/quasar-bridge/tts-pacing.jsonl`, not user audio or spoken text.
- If the backend supports only a different streamed audio codec/format than the existing Opus decoder, the new path may fail audibly; inspect `maind` playback / Bridge error logs before further changes. No unsupported firmware flags were edited.

## Rollback

**Instant for the next streamed conversation, without restarting:** remove the feature sentinel `/data/opt/quasar-bridge/enable-native-streaming-tts`. Every `streamSpeech` invocation checks it, so the next request returns to known-good 0.4.5d-style per-sentence native TTS behavior. Leave in-flight voice messages to finish or stop them normally.

**Full binary rollback:** local 0.4.5d build at `_tmp/native-cutover/quasar-bridge-0.4.5d-natural-pauses-linux-arm64`, on device at `/tmp/quasar-bridge-0.4.5d-rollback` until reboot (SHA256 `420688fac8a23853f832e1afcdc2141ae5a140a03ee387072f9cb56a58ece815`). Stop Bridge via service, restore old executable, restart service, confirm health. Deployment script `_tmp/native-cutover/deploy-native-stream-20261104.sh` automatically restores previous executable if health fails at deploy.

## Provenance

Definitive native UniProxy event fields and live network proof: `docs/NATIVE_TTS_STREAMING_CONFIRMED_20261104.md`. No new Codex thread was created or resumed.
