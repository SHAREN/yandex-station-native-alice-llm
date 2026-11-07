# Подготовка включения Search/HA на Яндекс Станции «Комната», 07.11.2026

**Статус: продакшен НЕ обновлён; оба новых модуля не активированы.**

## Фактически завершено

- Исходники `internal/assistant/searchrouter` + `web_search` + штатная потоковая озвучка при явном запросе поиска: тесты на мок-серверах проходили.
- Добавлено безопасное чтение двух ключей из root-only `/data/opt/secrets/web-search.env` без копирования ключей в код, Git, конфиг, CLI и логи: `internal/assistant/search_secrets.go`, unit-test. Наличие двух переменных на колонке подтверждено; API с самими ключами не проходили отдельного успешного вызова.
- `tools/go-cross-build.ps1 -Strip` создаёт корректный stripped ELF ARM64. Компактный, ещё не установленный кандидат на Home PC: `_tmp/quasar-bridge-search-ha-20261107-linux-arm64` SHA256 `9a925d569e429c8ef455e837d39019b34e180b365da25499c5475806922a3174`, размер 16 908 448 байт (~16,1 МиБ). Старый, установленный образ `_tmp/quasar-bridge-sentence-tts-20261105-linux-arm64` SHA256 `5ba6584fc1bff9aa751dda3902b29b7ed41b7104de437392ade90a7d2ecf067f` лежит на Home PC и проверен.
- Бинарник передавался только во временную RAM колонки через штатную проверенную SSH identity (без открытых LAN-портов); SHA256 кандидата совпал, и временная конфигурация `assistant_tools: {enabled:true,search:true}` прошла проверку `-check-config`.
- **После неуспешной приёмки временные кандидат и конфиг удалены** из `/tmp` колонки. Постоянные разделы, бинарник, `config.json`, `S98zzbetteryam`, штатная прошивка, TTS/ASR, web-search secret не менялись. Финальная health-check: Bridge `status=ok`, `assistant_tools_enabled=false`, `home_assistant_enabled=false`, свободно ~38,6 МиБ, 53% занято, `/tmp` почти пуст.
- Тестовый запуск кандидата с `-query` получил LLM HTTP 502. Доказательство: HA app `local_yandex_gemini_gateway` (Gateway 0.2.2) logged `conversation error: Quasar LLM HTTP 502: upstream HTTP 429` в 18:44:48, 07.11.2026. Повторный `-check-backends` на **прежнем продакшен-бинарнике** в 18:50 также не прошёл. Это upstream rate limit/квота, **не** результат нового кода, не дефект секретного файла Brave/Tavily.
- Попытка запустить временный HTTP download-сервер на Home PC не прошла из-за Windows Firewall; запрос на узкое scoped firewall-правило вернулся `Access Denied`. Временный HTTP-сервер **остановлен**, новых firewall правил не установлено.
- GitHub search-main features уже в коммитах `b574d6b`, `0db48e6`; изменения 07.11 к чтению root-secret и компактной сборке должны быть отдельным коммитом. Ни один API-ключ не коммитить.

## Home Assistant всё ещё не готов

- Действующий `ha_mcp_tools` in-process server на HA `192.168.0.136:9584` жив, но настроен с `webhook_auth=ha_auth` — нужен authenticated account/OAuth, нельзя отключать проверку или публиковать admin capability URL на колонке.
- Попыток меняющих HA-настройки/выключающих `ha_auth` не было. `QUASAR_HOME_MCP_URL` в актуальном Bridge не установлен. Не давать колонке административный токен целого HA. Перед включением нужен разрешённый ограниченный механизм (например, отдельный MCP endpoint для разрешённых Assist entities с authenticated user) и тест чтения состояний + безопасного управления.
- Старые быстрые фразы `включи свет`/`выключи свет` продолжает обрабатывать отдельно `alice-ha-fastcommands`; при расширении не дублировать через GPT и не отключать технический центральный выключатель комнаты.
- Пока LLM даёт upstream 429, **не выполнять перезапуск производственного Bridge** под видом «готово». Вернуться после успешного `-check-backends` действующей модели, валидировать ограниченную HA авторизацию, провести два real API запроса и production smoke тест родного TTS.

## Строгие правила выпуска

- Перед каждой записью: `runbooks/yandex-stations/check_room_storage.py`, не <25 МиБ после обновления, только временное `/tmp` staging, old-sha проверен, rollback существует **на Home PC**, не на колонке.
- Для новой сборки `-Strip` уменьшает размер с ~22,9 до ~16,1 МиБ без отключения функций.
- Перед deploy — проверить запуск поиска при запросе и отсутствие поиска в обычном разговоре, фактические Brave→Tavily fallback и monthly quota; затем только штатный потоковый TTS, healthz и аудио.
- Никаких секретов в public Git или пользователю, не логировать raw HTTP response от API, не включать PAYG.
