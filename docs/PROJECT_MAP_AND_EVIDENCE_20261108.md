# Карта наших наработок Яндекс Станции: проекты, факты и границы — 08.11.2026

Назначение: **индекс источников и карта проверенности** для будущих ChatGPT/Codex-агентов, а не новый установщик. Составлено по исходникам и README репозиториев, октябрьскому Codex-эксперименту root, локальным runbook и состоянию 04–07 ноября. Часть прежних ChatGPT-веток недоступна для полного повторного чтения из-за ограничения запросов к истории; там, где факт не подтверждён кодом или тестом, указан уровень уверенности.

## Карта репозиториев

| Место | За что отвечает | Важная граница |
| --- | --- | --- |
| [Yandex Station Extended](https://github.com/SHAREN/yandex-station-extended) | Наш форк общественной вики по platform/root/OTA/SSH, штатной прошивке, голосовому тракту и проектам сообщества | Это **документация**, а не наша кастомная прошивка. См. [проверенный кейс Lite 1](https://github.com/SHAREN/yandex-station-extended/blob/main/docs/start/lite1-root-case-20261027.md). |
| [Quasar Bridge](https://github.com/SHAREN/quasar-bridge) | Форк [skrashevich/quasar-bridge](https://github.com/skrashevich/quasar-bridge): маршрутизация Yandex Quasar/UniProxy и LLM/HA | Не считать каждый эксперимент форка установленным на станцию. |
| [Native Alice + Custom LLM](https://github.com/SHAREN/yandex-station-native-alice-llm) | **Канонический** source/build/docs для действующего текстового GPT-ассистента на Lite 1 и родного UniProxy TTS | Проверенная базовая версия `sentence-tts-20261105`; штатный `maind` не менялся. |
| [Fastcommands](https://github.com/SHAREN/yandex-station-ha-fastcommands) | Отдельный Go-мост локальных штатных command-spotter событий из `aliced` в LAN Home Assistant webhook | Две проверенные фразы без wake word; `allowed_commands` **не** обучает новые слова; учитывать double-run/fail-open. |
| `SHAREN/yandex-station-gemini-bridge` (приватный) | Ранние опыты `vqe_0`, Silero VAD, аудио Gemini Live, штатный PCM player, full-duplex | Исследовательская ветка, **не путать** с текущим Native Alice + GPT. Её README/CURRENT_STATE на 30.10 устаревают относительно более поздних сообщений; не утверждать, что полноценный Live уже работает на физической колонке. |
| [Home Assistant Voice Stack](https://github.com/SHAREN/home-assistant-voice-stack) | Отдельный домашний голосовой стек/бывшие экспериментальные codec snapshots | Не переносить туда активные эксперименты рутированной колонки. |

## Что точно было проверено на реальной Lite 1

1. **Root после factory и OTA** 27.10: `0.4.1.5…factory` → подписанный `0.369.1.46.4778262442.20261013.297` → активный root ADB → SSH root с ключом → контрольный reboot, OTA off, обычный DNS. Полные нюансы: [история root/прошивки](ROOT_AND_FIRMWARE_FIELD_HISTORY_20261108.md).
2. **Stock voice:** сохраняются родные wake-word, локальные споттеры, VQE/AEC, облачные SpeechKit ASR и Yandex UniProxy TTS/аудиоплеер. Статические строки `is_whisper`/`asr_whisper`, найденные в `maind`, **не доказывают локальное распознавание шёпота**. Нужно разделять бинарные подсказки и событийную трассировку.
3. **Native streaming TTS**, эталон 05.11: первые слова передаются в `DeferredTtsGenerate`, первый смысловой фрагмент до естественной паузы, остальные — по законченным предложениям. Одна UniProxy сессия/один `TtsSpeak` может содержать несколько логических Ogg/Opus-сегментов; задержки акустических стыков не равны стопроцентно гладкому вокодеру. Сборка подтверждена SHA, тестами, healthz и положительной прослушкой владельцем.
4. **Быстрые команды без слова активации:** отдельный `alice-ha-fastcommands` из локального command spotter отправляет две точные команды в HA. Обход ASR/LLM возможен потому, что распознавание этих фраз есть **в штатной прошивке**, а не потому, что наш bridge обучил модель.
5. **Реальная очистка станции** 07.11: 33 неиспользуемые резервные копии, сверенные по SHA, перенесены за пределы flash; зафиксирована политика не хранить старые версии на колонке. Опасно следовать устаревшему rollback, ожидающему `/tmp/quasar-bridge-0.4.5f.gz`.

## Исследования, которые нельзя выдавать за готовые функции

- **Gemini Live/full-duplex.** В соседних экспериментах исследовались `vqe_0` после штатной обработки, усиление ×32, Silero VAD (stop_secs=0.5), ошибки фрагментации речи native VAD (0,2–0,5 с), watchdog, остановка через `TextInput`, дубли от старого параллельного hybrid-assistant и маршрут через штатный `PCM_STREAM`. Некоторые этапы были программно проверены, но единого доказательства стабильного физического full-duplex с barge-in **нет**. Приватные `docs/ARCHITECTURE.md`, `BLOCKERS.md`, `DECISIONS.md` — исторические 30.10, а не текущий runtime.
- **Шёпот.** Наличие `is_whisper` и `asr_whisper` в бинарнике — основание искать события/флаги в логах, а не утверждать, что классификатор выполняется локально.
- **TTFT и звуковая задержка.** Штатная активация (earcon) и команды громкости/стопа должны оставаться в прошивке; внешнему агенту не следует дублировать их. Измерения времени проигрывания первого звука, холодный старт, остановка потока и stop/barge-in требуют отдельного A/B-теста; цель 1–2 с ещё не доказана.
- **Поиск и прямой HA.** Код `searchrouter`, встроенный `web_search` и локальный HA-адаптер разрабатывались и проходили offline-тесты. Но по [gate 07.11](SEARCH_HA_RELEASE_GATE_20261107.md) **не активированы на установленном Bridge**. Поисковые ключи могут находиться в root-only `web-search.env`, но наличие ключей ≠ работающий поиск. Не обходить ошибки авторизации, CodexPro ограничения записи секретов или Home Assistant `ha_auth`.
- **Сохранение журналов.** `conversation_log` включён на Lite, текстовые события ограничены размером; постоянный 30-дневный off-device сбор не настроен. Не смешивать журнал текстовых диалогов с непрерывной аудиозаписью.

## Источники для каждого типа будущих задач

| Если задача о… | Начать с… |
| --- | --- |
| Root, OTA, совместимость, brick/recovery | [проверенная история](ROOT_AND_FIRMWARE_FIELD_HISTORY_20261108.md), публичный [Lite кейс](https://github.com/SHAREN/yandex-station-extended/blob/main/docs/start/lite1-root-case-20261027.md), [wiki root](https://hackalice.me/start/root/), модельные разделы |
| Подключение к «Комнате» и изменения on-device | Приватные `runbooks/yandex-stations/ALICE_ROOM_ACCESS.md`, `ROOT_METHOD.md`, `STATION_STORAGE_POLICY.md` на Home PC; использовать установленный `alice-room` helper, не старый IP/сырые SSH-команды |
| Что сейчас развёрнуто | [CURRENT_STATE.md](CURRENT_STATE.md) с актуальной датой, healthz на реальном устройстве, конфигурация/хеш действующего бинарника; при конфликте исторической заметки и новой проверки побеждает проверка |
| TTS/проигрывание/протокол | [ARCHITECTURE](ARCHITECTURE.md), [streaming confirmed](NATIVE_TTS_STREAMING_CONFIRMED_20261104.md), [voice capture](https://github.com/SHAREN/yandex-station-extended/blob/main/docs/internals/voice.md) |
| Прямые команды дома | [fastcommands README](https://github.com/SHAREN/yandex-station-ha-fastcommands), [FAST_HOME_INTENTS_RESEARCH](FAST_HOME_INTENTS_RESEARCH_20261106.md), [HA release gate](SEARCH_HA_RELEASE_GATE_20261107.md) |
| Поиск и состояние HA | [SEARCH_HA_RELEASE_GATE](SEARCH_HA_RELEASE_GATE_20261107.md), [BRAVE_TAVILY_FAILOVER_RESEARCH](BRAVE_TAVILY_FAILOVER_RESEARCH_20261106.md), [DIRECT_HA_AND_SEARCH](DIRECT_HA_AND_SEARCH_20261106.md) |
| Gemini Live | Исторические и закрытые доки `SHAREN/yandex-station-gemini-bridge`, строго отдельно от текущей GPT/ASR/TTS реализации |

## Правила документирования

- Прямо помечать каждое утверждение: **проверено на колонке**, **offline-тест/исходник**, **вывод из прошивки**, **идея/гипотеза** или **устарело**.
- Писать дату, модель, platform, прошивку, версию binary, SHA, исходное и финальное состояние, откат и критерии теста.
- Не переписывать прошлые экспериментальные провалы задним числом: в хронологии сохранять «тогда не работало → позже заработало».
- Любые бэкапы, аккаунтные данные, точные ID устройств, аудиолог и ключи — **в приватный внешний архив, а не в wiki/GitHub**.
- При исследовании upstream и чужих вопросов в Telegram давать ссылку и предлагать **черновик ответа**, не публикуя от имени владельца без разрешения.
