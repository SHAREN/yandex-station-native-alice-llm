# Confirmed: one-session incremental text TTS via stock Yandex UniProxy — 2026-11-04

## Verdict
**YES: the currently reachable Yandex UniProxy endpoint accepts an Alice AAPI `TTtsGenerate(IsStreaming=true)` followed by one or more `TDeferredTtsGenerate` messages, and streams Opus audio before receiving the final text chunk.** The response audio remains on one stream ID and closes only when the final chunk is sent. This was demonstrated by a controlled live network experiment using the physical Lite 1's existing Bridge as a read-only forwarding transport; the production Station firmware, installed Bridge binary and speaker playback were not modified.

This verifies the server-side/native-AAPI capability, not yet the exact subjective prosody or a deployed client implementation. **It is not the external Yandex Cloud SpeechKit v3 gRPC `StreamSynthesis`** and needs no separate Cloud SpeechKit account to use the existing UniProxy transport.

## Exact protocol
Authoritative source: `v3rt3p/quasar-protos` commit `047f930157c934ca2d7811fd0cf68bb0c5f7d1c4`:
- `alice/protos/api/alicekit/protocol/client/client_message.proto`: `TClientMessage.TEvent.TtsGenerate` = **field 13**; `TClientMessage.TEvent.DeferredTtsGenerate` = **field 34**.
- `alice/protos/api/alicekit/events/tts_generate/tts_generate.proto`: `TTtsGenerate.Text` = field **7**, `TTtsGenerate.IsStreaming` = field **12** (bool).
- `alice/protos/api/alicekit/events/deferred_tts_generate/deferred_tts_generate.proto`: `TDeferredTtsGenerate.RequestMessageId` = field **1**, `Text` = **2**, `IsLast` = **3**, `ChunkNum` = **4**.
- Wrap each in regular `TClientMessage.Event` field 1, `TEvent.Header` field 1 and a unique header `MessageId`. Set `RequestMessageId` on every deferred chunk to the *initial* TtsGenerate message ID. Send `ChunkNum=1` and `IsLast=false` for an intermediate chunk; `ChunkNum=2` and `IsLast=true` for the last. Plain `TtsGenerate` without `IsStreaming` does not append later chunks.
- The working 2026-11-04 `freequasar/quasar-go-genproto` dependency in our Bridge is **older** and has no generated `IsStreaming` or `DeferredTtsGenerate`; update the generated schema or use a well-tested `protowire` compatibility extension.

The 2026-11-04 earlier attempt to interpret the adjacent value in the firmware `.data.rel.ro` name table as a tag was **incorrect**: the apparent `kTtsGenerate=34`, `kDeferredTtsGenerate=14`, etc. represented *the next entry's field number* because the raw table was read from the wrong offset. The authoritative `.proto` plus successful live wire test settle the actual tags: **13 and 34**. Old claims of conflicting tags or impossible protocol must not be repeated.

## Experiment details (real production UniProxy, separate isolated WebSocket)
Transport: Windows Python `websocket-client` to `ws://127.0.0.1:18443/uni.ws`, ADB-forwarded to physical Lite 1 `127.0.0.1:8443`, through the current Bridge to the existing Yandex UniProxy upstream. **Dedicated isolated websocket**, not the original maind/Bridge application session. ADB forward removed immediately after tests. Only synthetic harmless test sentences. No audio, device credentials or server binary frames saved; report only control-message categories, byte lengths, timing and stream IDs. Test harness: `_tmp/firmware-tts-audit-20261104/probe_ws.py` (temporary, not a production program).

| Test | Inputs | Observed server reply |
|---|---|---|
| Ordinary baseline | `TtsGenerate` without `IsStreaming` | 1 `directive`, 1 binary Opus frame (~14,436 bytes incl. stream prefix), 1 `StreamControl`; total 14,848 bytes |
| Ordinary baseline + deferred message | same, followed by `DeferredTtsGenerate` last | **Exactly same** output as plain baseline, proving that the streaming flag is essential |
| Streaming but never finished | initial `TtsGenerate(IsStreaming=true)`, without deferred | no sound frames within 9 seconds, waiting for continuation/finalization |
| Streaming + single last chunk | initial `TtsGenerate(IsStreaming=true)`, then `DeferredTtsGenerate(IsLast=true,ChunkNum=1)` | 4 `directive` frames, 4 binary audio frames, 1 `StreamControl`, total **32,624 bytes**, stream ID = 2 |
| Streaming + empty first text | initial `TtsGenerate(IsStreaming=true,Text="")`, then `DeferredTtsGenerate(IsLast=true,ChunkNum=1)` | Audio generated successfully, one audio stream, 32,608 total bytes |
| **Real streaming: intermediate and final chunks** | initial `TtsGenerate(IsStreaming=true,Text=...)`; deferred `ChunkNum=1,IsLast=false`; wait ~3.5s; deferred `ChunkNum=2,IsLast=true` | **Before final chunk**: 5 frames / **32,504 bytes**, including an Opus binary data frame. **After final chunk**: 6 frames / **23,557 bytes**; total **56,061 bytes**, 4 binary audio frames, **one stream ID = 2**, **one final StreamControl close**. This is decisive evidence of incremental synthesis *before* end-of-text. |

The sample text in this test is newly composed in the source code; no user's prior conversation or transcript was submitted. We did not persist actual synthesized audio, so subjective continuity of sentence intonation is **not yet proven**. A single continuing stream and an early binary audio reply show the network protocol can do genuine one-session text-to-speech streaming.

## What implementation would require
1. Preserve the stock `maind`, spotter, TTS audio player, status and volume controls. Do not change read-only firmware.
2. Change only the Quasar Bridge's **client-side TTS transport**: at first completed natural clause, send `TtsGenerate{Text:first,IsStreaming:true}` once, preserving its message ID; then send all LLM text deltas as `DeferredTtsGenerate` events on the **same original request** (increase `ChunkNum`). On LLM completion send the final text with `IsLast=true` and correct chunk numbering. No separate `TtsGenerate` for each sentence.
3. Handle server-stream output: the existing bridge mux assumes independent `TtsSpeak` streams per TTS request and concatenates local Ogg sources; in the new mode it must accept one TTS stream's multiple audio chunks, source close and finalize exactly once. Validate first-binary audio before final text and response continuity in a mock integration test.
4. For a first long sentence, only use an early comma/clause if it is useful for latency; **the streaming server now owns the text continuation and may preserve prosody better than independent TTS calls**. Whether it actually preserves inter-chunk *intonation* is to be confirmed with a physical listening test.
5. Include a feature flag, end-to-end instrumentation and fallback to the known-good 0.4.5d sentence-by-sentence mode on backend stream timeout / protocol mismatch. Do not switch production as part of this audit.

## Production state after this research
The speaker remains on Quasar Bridge 0.4.5d SHA-256 `420688fac8a23853f832e1afcdc2141ae5a140a03ee387072f9cb56a58ece815`, health `status=ok`, rootfs read-only. No new Codex threads or app deployments were started. The source document `docs/NATIVE_TTS_STREAMING_AUDIT_20261104.md` is now superseded as to its earlier unresolved server hypothesis.
