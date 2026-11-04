# Native Alice: prosody audit on UniProxy — 2026-11-04

## Context
User reports a perceptible intonation restart at every GPT text delta. The deployed Bridge 0.4.5f batches around 16 Unicode characters and sends each batch using DeferredTtsGenerate on one TtsGenerate(IsStreaming=true) request ID.

## Controlled isolated experiment
One original synthetic Russian paragraph was supplied in three ways through Yandex's real UniProxy using an isolated WebSocket (ADB-forwarded to the Station's existing Bridge). No physical device, GPT relay, installed Bridge or firmware modifications were made. Synthetic audio traces are only local ignored files in _tmp/prosody-native-20261104, and are not committed.

- One DeferredTtsGenerate carrying the whole text: one logical Ogg/Opus bitstream; 1 serial ID, 1 OpusHead, 1 EOS, 11.571 seconds, 107,871 encoded bytes, 60 Ogg pages.
- Three DeferredTtsGenerate inputs, at sentence boundaries: three independent logical Ogg/Opus bitstreams; 3 serial IDs, 3 OpusHead, 3 EOS, 11.165 seconds summed, 108,736 encoded bytes, 64 Ogg pages.
- Ten small DeferredTtsGenerate inputs, about 16 characters each: **ten independent logical Ogg/Opus bitstreams**; 10 serial IDs, 10 OpusHead, 10 EOS, 13.794 seconds summed, 133,877 encoded bytes, 95 Ogg pages.

All three used exactly one initial TtsGenerate with IsStreaming=true and the same request ID for all subsequent deferred messages. The network audio stream ID remained one; it does NOT imply one unbroken neural synthesis. The tiny case added ~2.22 seconds of generated audio duration for identical words. Ogg structure strongly supports each deferred input being synthesized as an independent acoustic segment, consistent with perceived prosody resets. Acoustic duration alone cannot prove subjective inflection quality, but server-level Ogg boundaries are incontrovertible.

The protocol tests include a server-generated TTS.Speak directive with **lazy_tts_streaming: false**. The meaning of LazyTTS and how to activate it is not established. The existence of the symbol in firmware is not proof of a different mode preserving intonation.

## Native firmware details
The exact stock Lite 1 maind binary SHA256 is 2dad0ffc61ba73d15ea813b754d7a56866d0477c321dcad9138b8d06e14ddbf1. Its generated event table includes client-to-server TDeferredTtsGenerate, while server-to-client is TtsSpeak with Opus stream. Other symbols: lazy_tts_streaming, ttsLazyStreaming, TActionLazyTTS, TtsUpdateState.

Published v3rt3p/quasar-protos definitions:
- TTtsGenerate has IsStreaming field 12; RealtimeStreamer is reserved.
- TDeferredTtsGenerate carries RequestMessageId, Text, IsLast and ChunkNum.
- TTtsSpeak has LazyTtsStreaming, SoundChunkType and SubStreamId.
- TTtsUpdateState is an outgoing playback-state report, NOT proven to select an alternative synthesizer.

Public Yandex SpeechKit v3 StreamSynthesis / LiveTTS supports incrementally arriving text using another API/authentication/voice, but it does not prove that this speaker's standard Yandex Alice voice is available through that API.

## Consequences and next steps
Do not claim that one UniProxy network stream equals one prosodically fluent utterance. Do not add packet stitching or punctuation heuristics as a substitute for verifying native cloud synthesis semantics. Examine actual stock Alice dialogue protocol for LazyTtsStreaming=true, TActionLazyTTS, SoundChunkType, SubStreamId and TtsUpdateState, determine whether these change only audio transport or also cloud prosody. Find and validate a genuine same-voice incremental synthesizer mode before replacing production. Any text buffering on natural boundaries should be labelled a workaround with a latency-versus-naturalness tradeoff.

Production Quasar Bridge 0.4.5f remains unchanged. No Codex Threads created.

References:
https://github.com/v3rt3p/quasar-protos
https://hackalice.me/internals/voice/
https://aistudio.yandex.ru/ru/docs/speechkit/tts/api/tts-streaming
