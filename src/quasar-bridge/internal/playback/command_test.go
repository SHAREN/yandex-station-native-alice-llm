package playback

import "testing"

func TestVoicePlaybackCommands(t *testing.T) {
	for _, tc := range []struct{ text, action string }{
		{"Следующий трек", "next"}, {"Алиса, включи следующую песню!", "next"}, {"Переключи на следующий трек", "next"}, {"Дальше", "next"},
		{"Следующая песня", "next"}, {"Вперёд", "next"}, {"Предыдущая песня", "previous"}, {"Назад", "previous"}, {"Сначала", "restart"},
		{"Предыдущий трек", "previous"}, {"Включи предыдущую песню", "previous"}, {"Верни предыдущий трек", "previous"},
		{"Поставь музыку на паузу", "pause"}, {"Пауза", "pause"}, {"Приостанови воспроизведение", "pause"},
		{"Продолжи", "resume"}, {"Продолжай музыку", "resume"}, {"Сними с паузы", "resume"}, {"Возобнови воспроизведение", "resume"},
		{"Хватит", "stop"}, {"Стоп", "stop"}, {"Останови музыку", "stop"}, {"Выключи музыку", "stop"},
		{"Начни песню сначала", "restart"}, {"Включи текущий трек сначала", "restart"},
	} {
		if got := Command(tc.text); got != tc.action {
			t.Errorf("%q: got %q want %q", tc.text, got, tc.action)
		}
	}
	for _, text := range []string{"Не переключай трек", "Как включить следующий трек", "Расскажи про предыдущую песню", "Продолжи объяснять код", "Поставь таймер на паузу", "Включи музыку с сайта audio.example/music", "Следующий вопрос", "Назад в историю", "Хватит ли памяти", "Включи свет", "Громче", "Не продолжай музыку"} {
		if got := Command(text); got != "" {
			t.Errorf("false action %q: %s", text, got)
		}
	}
}
