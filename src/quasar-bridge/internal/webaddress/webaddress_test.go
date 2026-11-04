package webaddress

import "testing"

func TestNormalizeWrittenAndSpokenAddresses(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"включи музыку с сайта abc.su/music", "включи музыку с сайта https://abc.su/music"},
		{"Открой https://example.org/Music?x=1", "Открой https://example.org/Music?x=1"},
		{"открой abc.ru/песни", "открой https://abc.ru/%D0%BF%D0%B5%D1%81%D0%BD%D0%B8"},
		{"Включи музыку с сайта абв точка су слэш music", "Включи музыку с сайта https://abv.su/music"},
		{"поставь музыку с бгд dot com slash music", "поставь музыку с https://bgd.com/music"},
		{"включи аудио с сайта абв точка с ю слеш Music", "включи аудио с сайта https://abv.su/Music"},
		{"с сайта абв.зу/music", "с сайта https://abv.su/music"},
		{"с сайта абв.zu/music", "с сайта https://abv.su/music"},
		{"открой пример.рф/Музыка", "открой https://xn--e1afmkfd.xn--p1ai/%D0%9C%D1%83%D0%B7%D1%8B%D0%BA%D0%B0"},
		{"на сайте пример точка рф слеш music", "на сайте https://xn--e1afmkfd.xn--p1ai/music"},
		{"открой пример.com/music", "открой https://xn--e1afmkfd.com/music"},
		{"с сайта www точка abc точка org слеш audio слеш Track.mp3", "с сайта https://www.abc.org/audio/Track.mp3"},
		{"ссылка abc.su/music, спасибо", "ссылка https://abc.su/music, спасибо"},
		{"почта user@example.org", "почта user@example.org"},
		{"число 3.14", "число 3.14"},
		{"проиграй файл Track.mp3", "проиграй файл Track.mp3"},
		{"включи музыку с сайта", "включи музыку с сайта"},
		{"адрес абв точка где то", "адрес абв точка где то"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := Normalize(tc.input); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if got := Normalize(tc.want); got != tc.want {
				t.Fatalf("normalization is not idempotent: %q", got)
			}
		})
	}
}

func TestMusicRequestNeedsPlaybackMusicAndWebsite(t *testing.T) {
	for _, text := range []string{
		"включи музыку с сайта abc.su/music",
		"проиграй песни со страницы https://example.org/audio",
		"Воспроизведи аудио абв точка су слэш music",
		"поставь музыку с сайта",
	} {
		if !MusicRequest(text) {
			t.Errorf("missed explicit website playback: %q", text)
		}
	}
	for _, text := range []string{
		"включи музыку", "включи радио", "включи песню",
		"какая музыка на сайте abc.su/music", "прочитай сайт abc.su/music",
		"не включай музыку с сайта abc.su/music", "не надо воспроизводить музыку с сайта abc.su/music",
		"как включить музыку на сайте abc.su/music", "объясни, как включить музыку с сайта abc.su/music",
	} {
		if MusicRequest(text) {
			t.Errorf("unexpected playback request: %q", text)
		}
	}
}
