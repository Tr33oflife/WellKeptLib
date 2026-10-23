package core

// lang.go — offline, stdlib-only subtitle language detection.
//
// Two explainable signals, no models, no downloads:
//  1. Unicode script of the letters ( Cyrillic / Arabic / Hebrew /
//     Greek / Han / Kana / Hangul / Thai / Devanagari dominate => that
//     language family, with a documented default per script).
//  2. For Latin-script text, stopword-frequency scoring against small
//     built-in stopword lists for en/es/fr/de/it/pt/nl, plus diacritic
//     hints (ñ -> es, ß -> de, ã/õ -> pt, ...).
//
// Confidence is the winning score's share of all language scores, so a
// genuinely mixed/ambiguous sample honestly reports low confidence and
// the audit marks it "uncertain" instead of guessing.

// Detection is the result of DetectLanguage.
type Detection struct {
	Code       string  `json:"code"`       // ISO 639-1, "" when nothing credible
	Confidence float64 `json:"confidence"` // 0..1, top score share
	Words      int     `json:"words"`
	Script     string  `json:"script"`
}

// stopwords per language: high-frequency function words. The lists
// are deliberately DISJOINT (each word assigned to exactly one
// language — for cognates shared across Romance languages, to the
// language where it is most characteristic). That keeps the winning
// score's share meaningful: genuine single-language text scores high
// instead of bleeding points into every related language.
var stopwords = map[string]map[string]bool{
	"en": words("the,of,and,to,in,is,you,that,it,he,was,for,on,are,with,his,they,this,have,from,had,by,what,all,were,we,when,your,can,said,there,each,which,she,how,their,will,about,out,many,then,them,these,so,some,her,would,make,like,him,into,has,two,more,see,way,could,than,first,been,who,now,only,just,know,take,people,very,my,do,if,but,or,am,be,an,at,up,down,here,why,whom,once,again,off,too,own,same"),
	"es": words("el,los,las,de,del,la,una,son,no,nos,me,con,para,por,está,estás,estoy,esto,eso,esta,este,como,más,pero,sus,ya,en,cuando,muy,sin,sobre,también,hasta,hay,donde,desde,todo,ni,otra,otro,él,tanto,estas,estos,mucho,nada,cual,ella,estar,algunas,algo,nosotros,mi,mis,tú,ti,al,qué,se,es"),
	"fr": words("le,les,des,un,est,et,au,aux,ce,cette,je,tu,elle,nous,vous,ils,elles,ne,pas,qui,dans,sur,plus,pour,avec,sont,être,avoir,mais,ou,tout,tous,toute,sa,ses,leur,y,faire,fait,dit,peut,moi,toi,lui,quoi,quand,où,pourquoi,comment,très,bien,chez,entre,même,aussi,alors,après,avant,pendant,depuis,mon,mes,ton,ta,tes,notre,votre,vos,quel,quelle,cela,celui,as,maintenant,devons,partir,parce,moyen,longtemps,besoin,aurais,serais,dis,toujours,jamais,beaucoup"),
	"de": words("der,die,das,und,ist,ein,eine,einen,einem,einer,den,dem,zu,von,mit,sich,auf,für,nicht,ich,du,er,sie,wir,ihr,auch,als,bei,nach,wie,aber,aus,dann,doch,schon,noch,war,sind,haben,habe,hat,werden,wird,mein,meine,dein,seine,ihre,uns,euch,ihnen,mir,dir,ihm,über,unter,wenn,weil,dass,beim,vom,zum,zur,ins,ans,im"),
	"it": words("il,lo,gli,di,dello,della,dei,degli,delle,uno,che,è,sono,non,su,per,tra,fra,come,anche,ma,più,questo,questa,questi,queste,quello,quella,io,lei,noi,voi,loro,ci,vi,si,già,ancora,sempre,perché,cosa,chi,quanto,molto,poco,tutto,tutti,altro,altra,essere,avere,fare,può,voglio,sai,bene,ora,dopo,prima,mentre,contro,sotto,sopra,dentro,fuori,ogni,e"),
	"pt": words("o,os,da,dos,um,umas,em,na,nas,pelo,pela,com,sem,mas,ao,aos,às,seu,sua,seus,suas,meu,minha,ele,ela,eles,elas,nós,você,lhe,isso,isto,esse,essa,aquele,aquela,já,não,sim,muito,onde,pois,ser,ter,vai,vão,foi,era,depois,antes"),
	"nl": words("het,van,ik,jij,hij,zij,wij,u,dat,deze,aan,bij,met,voor,naar,over,onder,tussen,door,tegen,zonder,tot,op,uit,ook,nog,wel,niet,geen,welke,wat,waar,wanneer,waarom,hoe,veel,weinig,meer,minder,alle,alles,andere,zijn,hebben,heeft,heb,zullen,zal,kan,kunnen,moet,moeten,mag,mogen,wil,willen,doen,maar,want,dan,toen,nu,hier,daar,altijd,nooit,soms,vaak"),
}

// sharedStopwords are forms genuinely shared by several of the
// supported languages ("que" is Spanish, French and Portuguese).
// They score fractionally for each instead of being assigned to one
// language and leaking full points from the others' texts.
var sharedStopwords = map[string][]string{
	"que": {"es", "fr", "pt"},
}

// words builds a set from a comma-separated list (helper for the table
// above; keeps the lists readable).
func words(csv string) map[string]bool {
	m := make(map[string]bool)
	cur := ""
	for _, r := range csv + "," {
		if r == ',' {
			if cur != "" {
				m[cur] = true
			}
			cur = ""
		} else {
			cur += string(r)
		}
	}
	return m
}

// scriptOf returns the script name for a rune, or "" for Latin/other.
func scriptOf(r rune) string {
	switch {
	case r >= 0x0400 && r <= 0x04FF:
		return "cyrillic"
	case r >= 0x0600 && r <= 0x06FF:
		return "arabic"
	case r >= 0x0590 && r <= 0x05FF:
		return "hebrew"
	case r >= 0x0370 && r <= 0x03FF:
		return "greek"
	case r >= 0x4E00 && r <= 0x9FFF:
		return "han"
	case r >= 0x3040 && r <= 0x30FF:
		return "kana"
	case r >= 0xAC00 && r <= 0xD7AF:
		return "hangul"
	case r >= 0x0E00 && r <= 0x0E7F:
		return "thai"
	case r >= 0x0900 && r <= 0x097F:
		return "devanagari"
	}
	return ""
}

// scriptLanguage maps a dominant non-Latin script to its default ISO
// code. Cyrillic is genuinely ambiguous (ru/uk/bg/...); "ru" is the
// documented default and the confidence reflects the script share only.
var scriptLanguage = map[string]string{
	"cyrillic": "ru", "arabic": "ar", "hebrew": "he", "greek": "el",
	"han": "zh", "kana": "ja", "hangul": "ko", "thai": "th", "devanagari": "hi",
}

// tokenize splits text into lowercase letter-words (Unicode-aware via
// range checks, no regexp needed for the hot path).
func tokenize(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range text {
		if isLetter(r) {
			cur = append(cur, lowerRune(r))
		} else {
			flush()
		}
	}
	flush()
	return out
}

// isLetter reports whether r is a letter we count (Latin + the scripts
// in scriptOf + Latin-1 / Latin Extended letters).
func isLetter(r rune) bool {
	if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
		return true
	}
	if r >= 0x00C0 && r <= 0x024F { // Latin-1 supplement + Extended-A
		return true
	}
	return scriptOf(r) != ""
}

// lowerRune lowercases ASCII and the common Latin-1 capitals; other
// scripts are caseless for our purposes.
func lowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	if r >= 0x00C0 && r <= 0x00DE && r != 0x00D7 {
		return r + 32
	}
	return r
}

// DetectLanguage detects the language of a text sample. Deterministic:
// the same input always yields the same Detection.
func DetectLanguage(text string) Detection {
	toks := tokenize(text)
	det := Detection{Words: len(toks)}
	if len(toks) == 0 {
		return det
	}
	// Signal 1: script shares among letters.
	scriptCounts := map[string]int{}
	letters := 0
	for _, r := range text {
		if !isLetter(r) {
			continue
		}
		letters++
		if s := scriptOf(r); s != "" {
			scriptCounts[s]++
		}
	}
	bestScript, bestCount := "", 0
	for s, n := range scriptCounts {
		if n > bestCount {
			bestScript, bestCount = s, n
		}
	}
	if letters > 0 && bestScript != "" && float64(bestCount)/float64(letters) > 0.5 {
		det.Script = bestScript
		det.Code = scriptLanguage[bestScript]
		det.Confidence = float64(bestCount) / float64(letters)
		return det
	}
	det.Script = "latin"
	// Signal 2: stopword scores + diacritic hints.
	scores := map[string]float64{}
	for _, tok := range toks {
		if langs, ok := sharedStopwords[tok]; ok {
			for _, l := range langs {
				scores[l] += 1.0 / float64(len(langs))
			}
		}
		for lang, set := range stopwords {
			if set[tok] {
				scores[lang]++
			}
		}
		for _, r := range tok {
			switch r {
			case 'ñ':
				scores["es"] += 2
			case 'ß':
				scores["de"] += 2
			case 'ã', 'õ':
				scores["pt"] += 2
			case 'ä', 'ö', 'ü':
				scores["de"]++
			case 'ç':
				scores["fr"] += 0.5
				scores["pt"] += 0.5
			case 'à', 'è':
				scores["fr"] += 0.5
				scores["it"] += 0.5
			case 'ù':
				scores["it"] += 0.5
				scores["fr"] += 0.5
			}
		}
	}
	total := 0.0
	best, bestScore := "", 0.0
	for lang, s := range scores {
		total += s
		if s > bestScore {
			best, bestScore = lang, s
		}
	}
	if total == 0 || bestScore == 0 {
		det.Code = ""
		det.Confidence = 0
		return det
	}
	det.Code = best
	det.Confidence = bestScore / total
	return det
}

// langCodeNormal maps ISO 639-2 / bibliographic codes and names to the
// ISO 639-1 codes the detector returns ("" when unknown).
func langCodeNormal(s string) string {
	switch lowerASCII(s) {
	case "en", "eng":
		return "en"
	case "es", "spa":
		return "es"
	case "fr", "fre", "fra":
		return "fr"
	case "de", "ger", "deu":
		return "de"
	case "it", "ita":
		return "it"
	case "pt", "por":
		return "pt"
	case "nl", "dut", "nld":
		return "nl"
	case "ja", "jpn":
		return "ja"
	case "ko", "kor":
		return "ko"
	case "zh", "chi", "zho":
		return "zh"
	case "ru", "rus":
		return "ru"
	case "ar", "ara":
		return "ar"
	case "he", "heb":
		return "he"
	case "el", "gre", "ell":
		return "el"
	case "th", "tha":
		return "th"
	case "hi", "hin":
		return "hi"
	case "und":
		return "und"
	}
	return ""
}

// lowerASCII lowercases a short ASCII-ish string.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
