package translation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Map maps a target locale like "hi_IN" to the translation string
type Map map[string]string

// TranslateAPIURL is the base URL for Google Translate (set from config at startup)
var TranslateAPIURL = "https://translate.googleapis.com/translate_a/single"

var languages = map[string]string{
	"en_US": "en",
	"hi_IN": "hi",
	"te_IN": "te",
	"kn_IN": "kn",
}

var translateClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:    5,
		IdleConnTimeout: 90 * time.Second,
	},
}

// TranslateField concurrently translates the input text into the target languages.
func TranslateField(text string) Map {
	translations := make(Map)
	if text == "" {
		for key := range languages {
			translations[key] = ""
		}
		return translations
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	for key, code := range languages {
		wg.Add(1)
		go func(k, c string) {
			defer wg.Done()
			translated := fetchTranslation(translateClient, text, c)
			mu.Lock()
			translations[k] = translated
			mu.Unlock()
		}(key, code)
	}

	wg.Wait()
	return translations
}

func fetchTranslation(client *http.Client, text, code string) string {
	if code == "en" {
		return text // Base language is English
	}

	encodedText := url.QueryEscape(text)
	urlStr := fmt.Sprintf("%s?client=gtx&sl=auto&tl=%s&dt=t&q=%s", TranslateAPIURL, code, encodedText)

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return text
	}

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return text
	}
	defer resp.Body.Close()

	// The undocumented API returns a nested array, e.g., [[["Hola", "Hello", null, null, 1]], null, "en"]
	var result []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return text
	}

	if len(result) > 0 {
		if parts, ok := result[0].([]interface{}); ok {
			var translated string
			for _, part := range parts {
				if inner, ok := part.([]interface{}); ok && len(inner) > 0 {
					if str, ok := inner[0].(string); ok {
						translated += str
					}
				}
			}
			if translated != "" {
				return translated
			}
		}
	}

	return text
}
