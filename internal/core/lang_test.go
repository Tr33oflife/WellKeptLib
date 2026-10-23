package core

// lang_test.go — detector tests. The key safety property: non-English
// text must NOT detect as English (that is the whole audit).

import "testing"

// sample paragraphs, stopword-rich like real subtitle dialogue.
const (
	enText = "I do not know what you are talking about. You said that you would be here when I needed you, and now you are telling me that this is not your problem. Look, we have to go now, there is no way we can stay here any longer than we have to."
	esText = "No sé qué estás diciendo. Tú me dijiste que estarías aquí cuando te necesitara, y ahora me estás diciendo que esto no es tu problema. Mira, tenemos que irnos ahora, no hay manera de que nos quedemos aquí más tiempo del que tenemos que quedarnos, porque esto es muy peligroso para todos nosotros."
	frText = "Je ne sais pas de quoi tu parles. Tu as dit que tu serais là quand je aurais besoin de toi, et maintenant tu me dis que ce n est pas ton problème. Écoute, nous devons partir maintenant, il n y a pas moyen que nous restions ici plus longtemps que nous le devons, parce que c est très dangereux pour nous tous."
	deText = "Ich weiß nicht, wovon du sprichst. Du hast gesagt, dass du da sein würdest, wenn ich dich brauche, und jetzt sagst du mir, dass das nicht dein Problem ist. Hör zu, wir müssen jetzt gehen, es gibt keine Möglichkeit, dass wir länger hier bleiben, als wir müssen, weil das für uns alle sehr gefährlich ist und wir uns beeilen müssen."
)

// TestDetectEnglish detects the English paragraph as en.
func TestDetectEnglish(t *testing.T) {
	d := DetectLanguage(enText)
	if d.Code != "en" {
		t.Fatalf("code = %q (conf %.2f, words %d)", d.Code, d.Confidence, d.Words)
	}
	if d.Confidence < 0.75 {
		t.Fatalf("confidence too low: %.2f (audit threshold is 0.75)", d.Confidence)
	}
}

// TestDetectSpanish detects Spanish as es.
func TestDetectSpanish(t *testing.T) {
	d := DetectLanguage(esText)
	if d.Code != "es" {
		t.Fatalf("code = %q (conf %.2f)", d.Code, d.Confidence)
	}
	if d.Confidence < 0.75 {
		t.Fatalf("confidence too low: %.2f (audit threshold is 0.75)", d.Confidence)
	}
}

// TestDetectFrench detects French as fr.
func TestDetectFrench(t *testing.T) {
	d := DetectLanguage(frText)
	if d.Code != "fr" {
		t.Fatalf("code = %q (conf %.2f)", d.Code, d.Confidence)
	}
	if d.Confidence < 0.75 {
		t.Fatalf("confidence too low: %.2f (audit threshold is 0.75)", d.Confidence)
	}
}

// TestDetectGerman detects German as de.
func TestDetectGerman(t *testing.T) {
	d := DetectLanguage(deText)
	if d.Code != "de" {
		t.Fatalf("code = %q (conf %.2f)", d.Code, d.Confidence)
	}
	if d.Confidence < 0.75 {
		t.Fatalf("confidence too low: %.2f (audit threshold is 0.75)", d.Confidence)
	}
}

// TestSpanishNotEnglish is the audit's core safety property.
func TestSpanishNotEnglish(t *testing.T) {
	d := DetectLanguage(esText)
	if d.Code == "en" {
		t.Fatal("Spanish text detected as English")
	}
}

// TestDetectScript checks non-Latin script detection (Russian).
func TestDetectScript(t *testing.T) {
	d := DetectLanguage("Я не знаю, о чём ты говоришь. Ты сказал, что будешь здесь, когда ты мне понадобишься, и теперь ты говоришь мне, что это не твоя проблема. Слушай, нам нужно идти сейчас, потому что это очень опасно для всех нас.")
	if d.Code != "ru" || d.Script != "cyrillic" {
		t.Fatalf("got code=%q script=%q", d.Code, d.Script)
	}
}

// TestDeclaredLangFilename covers the filename language-suffix shapes.
func TestDeclaredLangFilename(t *testing.T) {
	cases := map[string]string{
		"/x/Movie.en.srt":         "en",
		"/x/Movie.eng.srt":        "en",
		"/x/Movie.en.forced.srt":  "en",
		"/x/Movie.en.sdh.ass":     "en",
		"/x/Movie.es.srt":         "es",
		"/x/Movie.spa.srt":        "es",
		"/x/Movie.fre.forced.vtt": "fr",
		"/x/Movie.srt":            "",
		"/x/Movie.2020.srt":       "",
		"/x/plainname.srt":        "",
	}
	for path, want := range cases {
		if got := declaredLangFromFilename(path); got != want {
			t.Errorf("declaredLangFromFilename(%q) = %q, want %q", path, got, want)
		}
	}
}
