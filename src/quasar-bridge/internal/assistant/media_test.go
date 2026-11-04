package assistant

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mediaFixtureLink struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}
type mediaResult struct {
	Links []mediaFixtureLink `json:"links"`
	Audio []mediaFixtureLink `json:"audio"`
}

func readMediaFixture(t *testing.T, body, contentType string) mediaResult {
	t.Helper()
	reader := NewWebReader(DefaultConfig())
	reader.http = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	result, err := reader.Read(t.Context(), "https://audio.example/album/page")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	var media mediaResult
	if err := json.Unmarshal(raw, &media); err != nil {
		t.Fatal(err)
	}
	return media
}

func TestWebReaderExtractsAudioAndNavigableLinks(t *testing.T) {
	result := readMediaFixture(t, `<html><title>Album</title><a href="/music">Music <b>collection</b></a><a href="javascript:alert(1)">bad</a><audio src="first.mp3" title="First"></audio><source src="/tracks/песня.mp3"><a href="first.mp3">duplicate</a><a href="http://127.0.0.1/private.mp3">private</a></html>`, "text/html")
	if len(result.Audio) != 2 || result.Audio[0].URL != "https://audio.example/album/first.mp3" || result.Audio[1].URL != "https://audio.example/tracks/%D0%BF%D0%B5%D1%81%D0%BD%D1%8F.mp3" {
		t.Fatalf("missing ordered, resolved, deduplicated public audio URLs: %+v", result)
	}
	if len(result.Links) == 0 || result.Links[0].URL != "https://audio.example/music" || result.Links[0].Title != "Music collection" {
		t.Fatalf("navigation lost: %+v", result.Links)
	}
}

func TestWebReaderExtractsAudioFromStructuredData(t *testing.T) {
	for _, fixture := range []struct{ contentType, body string }{
		{"application/json", `{"tracks":[{"title":"First","src":"/one.mp3"},{"name":"Second","contentUrl":"/two.mp3"}]}`},
		{"text/html", `<p>Album</p><script type="application/ld+json">{"@type":"MusicPlaylist","track":[{"name":"First","contentUrl":"/one.mp3"},{"name":"Second","contentUrl":"/two.mp3"}]}</script>`},
	} {
		result := readMediaFixture(t, fixture.body, fixture.contentType)
		if len(result.Audio) != 2 || result.Audio[0].URL != "https://audio.example/one.mp3" || result.Audio[0].Title != "First" || result.Audio[1].Title != "Second" {
			t.Fatalf("structured audio lost: %+v", result.Audio)
		}
	}
}

func TestWebReaderExtractsSplitFrameworkDataWithoutExecutingScripts(t *testing.T) {
	data := `a:["$","Player",null,{"tracks":[{"title":"First","src":"/one.mp3"},{"title":"Second","src":"/two.mp3"}]}]` + "\n"
	first, _ := json.Marshal([]any{1, data[:60]})
	second, _ := json.Marshal([]any{1, data[60:]})
	body := `<p>Album</p><script>self.__next_f.push(` + string(first) + `)</script><script>self.__next_f.push(` + string(second) + `)</script>`
	result := readMediaFixture(t, body, "text/html")
	if len(result.Audio) != 2 || result.Audio[0].Title != "First" || result.Audio[1].URL != "https://audio.example/two.mp3" {
		t.Fatalf("framework media data lost: %+v", result.Audio)
	}
}
