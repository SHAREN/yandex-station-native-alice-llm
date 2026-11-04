# Predictive Quasar TTS chunking policy (2026-11-04)

**Current production 0.4.5d extends this policy with natural punctuation boundaries.** First long sentences may now begin at a comma/semicolon/colon; no arbitrary word split. The 60% / 3-second scheduling formula below remains unchanged. See `docs/NATIVE_TTS_STREAMING_AUDIT_20261104.md`.

## What was actually measured
- Latest long story about Grigory, LLM prompt `Расскажи длинную историю`, 4,740 generated characters, first text token at 1.8773 s, response completed at 22.3697 s (LLM relay `/timing`).
- Existing firmware `maind` log for the same session: native TTS stream `TTS.Speak` received at 09:14:50.236 UTC, native state `onTtsStarted` at 09:14:50.312 UTC — 76 ms between these **firmware events**, **not** total synthesis time. The first two-part playback lacked a timing log for each spoken text fragment.
- No exact Yandex Alice voice reading rate or TTS latency can be extracted from those old logs alone. Do **not** present any model gaps as real physical measurements.
- External benchmark for Russian conversational speech: [SozAI 2026 sample of 2,884 Russian transcripts](https://sozai.app/speaking-rate-by-language/) found a median of 139 words/min. This is **not** a Yandex Alice firmware measurement. Yandex SpeechKit docs define synthesis speed `1.0` as average human speech rate: https://aistudio.yandex.ru/ru/docs/speechkit/tts/request

## Proposed/implemented conservative policy (Bridge, stock TTS)
1. Begin synthesizing the **first completed sentence immediately**, even if it is very short; do not add artificial introductory text to the model prompt.
2. Estimate audible duration `D ~= Unicode text characters / 14 characters/second` initially; **14 char/s is only a baseline**.
3. Start the following chunk's stock `TtsGenerate` preparation at `t = max(0, min(0.60 * D, D - 3 seconds))` counted from the previous request's TTS dispatch. Using dispatch rather than physical speaker start starts preparation **earlier**, conservatively.
4. For short segments `D <= 3 seconds` prefetch immediately. When the trigger fires, take 1–2 *already completed* sentences, combining two if the group fits within 220 Unicode characters; otherwise take one. Never wait for a second sentence if it has not finished.
5. The existing stock `TtsGenerate` / `TtsSpeak` mechanism remains authoritative; one audio output stream, ordered segments, no overlapping voices. The next stock synthesis is sent only once the previous stock synthesis source closes, as before — the existing streaming audio mux cannot safely buffer two parallel stock TTS sources.
6. Once each stock Opus segment is received, infer **actual voice duration** from its 48 kHz Opus sample count. Smooth the estimated speech rate between 6 and 30 characters/s, using 75% previous + 25% new value. Only timings and lengths are persisted (no voice recordings or spoken text) to `/data/opt/quasar-bridge/tts-pacing.jsonl`, enabling direct calibration in the next live test.
7. Stop/cancel uses the existing request context. Long stories keep the already-installed 12-minute streaming timeout. The result is meant to reduce silence and TTS request count, but cannot guarantee gapless playback before new device measurements.

## Reproducible simulation
Run `python3 _tmp/simulate_tts_prefetch_20261104.py`. This uses the *measured* 4,740 chars / 22.37 s / 1.877 s from the relay, plus an explicitly synthetic 50-sentence distribution with linear token generation, 14 chars/s voice, **assumed** 1-second TTS startup and alternative TTS synthesis speeds. Simulated silence means time between speaker chunks, not CPU or server idle.

| TTS backend: synth time as fraction of voiced duration | Old 2-part: estimated max silence | Existing per-sentence: requests / total silence | Predictive 60%, 3-sec reserve, 1–2 sentences: requests / total silence |
|---|---:|---:|---:|
| 16% (much faster than playback) | 18.37 s | 50 / 0 s | 31 / 0 s |
| 60% (faster than playback) | 18.37 s | 50 / 0.20 s | 31 / 0.20 s |
| 100% (exactly realtime TTS source) | 18.37 s | 50 / 49 s | 31 / 30 s |

**Critical limitation:** if the actual native TTS source produces audio at only realtime speed, *serial* TTS requests cannot prepare a new voice segment until the previous source finishes. In that case true parallel pre-synthesis would require a separate, carefully tested per-source audio buffer/mux; this update deliberately does not introduce such risky concurrent audio changes before collecting `tts-pacing.jsonl` device evidence.

## Why the policy matters
The model generates ~212 text characters/s during the measured story; the baseline speaker reads about 14 chars/s, so model text generation is roughly 15x faster than playback. Keeping already generated sentences in a bounded queue and triggering one native synthesis ahead avoids waiting for the entire model completion. Batching 1–2 sentences cuts the number of TTS requests and associated setup costs. The **first short sentence** is a special risk: 2 seconds of voice and a 1-second TTS startup leave almost no margin; prefetch begins immediately.

## Device verification still needed
- Check `tts-pacing.jsonl` after one real long story (per-segment duration, synthesis time, ratio and adaptive estimate).
- Confirm native Yandex TTS output can be generated ahead of audible playback and whether buffered silence is present.
- Check interruption `стоп`, `тише` and final audio close. If backend is realtime, stop at this measured limitation instead of falsely claiming the above eliminates all gaps.
