package main

import (
	"embed"
	"fmt"
	"log"
	"sync"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// The locale files are shared with the frontend, which bundles en.json and
// receives the others from GetLanguageState. The source hashes let i18n drop a
// translation of English that has changed since.
//
//go:embed frontend/src/locales/*.json i18n/source-hashes/*.json
var localeFiles embed.FS

func init() {
	if err := i18n.Register(localeFiles, "frontend/src/locales", "i18n/source-hashes"); err != nil {
		log.Printf("i18n: %v", err)
	}
}

// LanguageState is what the language picker and the frontend's first paint need.
type LanguageState struct {
	// Preference is the saved choice, or "" to follow the system.
	Preference string `json:"preference"`
	// Active is the locale in use now.
	Active string `json:"active"`
	// System is the locale the OS (or, failing that, the webview) asks for, or
	// "" when we ship none of its languages.
	System    string          `json:"system"`
	Languages []i18n.Language `json:"languages"`
	// Messages are Active's translations. English is bundled in the frontend
	// and fills every key missing here.
	Messages map[string]string `json:"messages"`
}

// languageMu keeps each read or save of the preference together with applying
// it. Wails runs every bound call on its own goroutine, so two quick picks could
// otherwise save one language and leave the other in use until a restart.
var languageMu sync.Mutex

// systemLocale is the OS's best supported locale, else the webview's.
func systemLocale(webview []string) string {
	if code := i18n.Match(i18n.Detect()...); code != "" {
		return code
	}
	return i18n.Match(webview...)
}

// applyLanguage sets the locale Go text uses and redraws the tray.
func (a *App) applyLanguage(preference string, webview []string) (system, active string) {
	system = systemLocale(webview)
	active = preference
	if active == "" {
		active = system
	}
	if active == "" {
		active = i18n.Default
	}
	i18n.SetLocale(active)
	if a.window {
		SetTrayLabels(i18n.T("tray.show"), i18n.T("tray.quit"))
	}
	return system, i18n.Current()
}

func (a *App) languageState(preference, system, active string) LanguageState {
	state := LanguageState{Preference: preference, Active: active, System: system, Languages: i18n.Languages()}
	if active != i18n.Default {
		state.Messages = i18n.Messages(active)
	}
	return state
}

// GetLanguageState returns the language to draw in. webview is the webview's
// navigator.languages, used only when the OS names no language we ship.
func (a *App) GetLanguageState(webview []string) (LanguageState, error) {
	if err := a.ready(); err != nil {
		return LanguageState{}, err
	}
	languageMu.Lock()
	defer languageMu.Unlock()
	preference := a.cfg.Config().Language
	system, active := a.applyLanguage(preference, webview)
	return a.languageState(preference, system, active), nil
}

// SetLanguage saves the user's choice: a code from i18n.Locales, or "" to follow
// the system. It takes effect at once.
func (a *App) SetLanguage(code string, webview []string) (LanguageState, error) {
	if err := a.ready(); err != nil {
		return LanguageState{}, err
	}
	if code != "" && i18n.Match(code) != code {
		return LanguageState{}, fmt.Errorf("%s", i18n.T("language.error.unsupported", "code", code))
	}
	languageMu.Lock()
	defer languageMu.Unlock()
	if err := a.cfg.Update(func(cfg *config.AppConfig) { cfg.Language = code }); err != nil {
		return LanguageState{}, err
	}
	system, active := a.applyLanguage(code, webview)
	return a.languageState(code, system, active), nil
}
