# Разделение двух проектов

## Native Alice + Custom LLM (этот репозиторий)

**Назначение:** работа через штатный стек Алисы, с иной текстовой нейросетью, но родным голосом, штатным ASR и встроенными функциями колонки. Основан на модифицированном Quasar Bridge, использует отдельный GPT-релей и подтверждённый Yandex UniProxy incremental TTS.

Локальный проект: `D:\codexpro_workspace\yandex-station-native-alice-llm`.

Содержимое: `src/quasar-bridge`, `src/quasar-llm-relay`, сборочные инструменты и документы Native TTS. **Независимая история Git с новым начальным коммитом**, отдельный origin.

## Gemini Live (другой репозиторий)

GitHub: `SHAREN/yandex-station-gemini-bridge`.
Старые каталоги: `D:\codexpro_workspace\yandex-station-gemini-bridge` и Git worktree `D:\codexpro_workspace\yandex-station-gemini-bridge-full-duplex`.

**Назначение:** Gemini Live, двунаправленное аудио и эксперименты с отдельным live-agent. Не развивать native Alice + GPT здесь.

## Границы

- Никаких Git-перенаправлений, force-push, reset или перезаписи истории старого репозитория.
- Исходный старый worktree сохранён без удаления незакоммиченных файлов. Это исторический источник кода, **не** основная площадка будущей разработки.
- Код live-agent, Gemini Live server, аудиозаписи, локальные бинарники, прошивки и дампы не переносились в новый Git.
- Код Quasar Bridge основан на Apache-2.0 проекте; LICENSE/NOTICE/THIRD_PARTY_NOTICES сохраняются.
- При следующих изменениях сначала менять/тестировать новый отдельный репозиторий, затем при необходимости собирать новую версию для устройства с rollback.
