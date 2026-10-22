package domain

import (
	"strings"
	"testing"
)

func TestValidateScreensPublicInventory(t *testing.T) {
	base := Screen{ID: "screen-1", Name: "Écran 1 (virtual)", Scope: "screen", Width: 1920, Height: 1080, Primary: true, Applications: []string{"browser", "com.example.Terminal"}}
	for _, screens := range [][]Screen{nil, {}, {base}, {{ID: "desktop", Name: "Whole desktop", Scope: "desktop", Width: 32768, Height: 1}}} {
		if err := ValidateScreens(screens); err != nil {
			t.Fatal("valid public screen inventory rejected", err)
		}
	}
	for _, test := range []struct {
		name string
		edit func(*Screen)
	}{
		{"empty ID", func(s *Screen) { s.ID = "" }},
		{"path ID", func(s *Screen) { s.ID = "/Users/worker/display" }},
		{"native display ID", func(s *Screen) { s.ID = ":0" }},
		{"long ID", func(s *Screen) { s.ID = strings.Repeat("a", 65) }},
		{"empty label", func(s *Screen) { s.Name = "" }},
		{"long label", func(s *Screen) { s.Name = strings.Repeat("a", 129) }},
		{"path label", func(s *Screen) { s.Name = "home/worker/display" }},
		{"URL label", func(s *Screen) { s.Name = "https://display.example" }},
		{"control label", func(s *Screen) { s.Name = "Display\n1" }},
		{"bidi label", func(s *Screen) { s.Name = "Display\u202e1" }},
		{"surrounding whitespace", func(s *Screen) { s.Name = " Display " }},
		{"invalid UTF-8 label", func(s *Screen) { s.Name = string([]byte{0xff}) }},
		{"unknown scope", func(s *Screen) { s.Scope = "window" }},
		{"zero width", func(s *Screen) { s.Width = 0 }},
		{"negative height", func(s *Screen) { s.Height = -1 }},
		{"unbounded geometry", func(s *Screen) { s.Height = 32769 }},
		{"empty application", func(s *Screen) { s.Applications = []string{""} }},
		{"application path", func(s *Screen) { s.Applications = []string{"/Applications/Browser.app"} }},
		{"application command", func(s *Screen) { s.Applications = []string{"browser --private"} }},
		{"duplicate application", func(s *Screen) { s.Applications = []string{"browser", "browser"} }},
		{"too many applications", func(s *Screen) { s.Applications = make([]string, MaxScreenApplications+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			screen := base
			test.edit(&screen)
			if err := ValidateScreens([]Screen{screen}); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("unsafe screen capability was accepted", screen, err)
			}
		})
	}
	duplicate := base
	duplicate.Primary = false
	secondPrimary := base
	secondPrimary.ID = "screen-2"
	for _, screens := range [][]Screen{{base, duplicate}, {base, secondPrimary}, make([]Screen, MaxScreens+1)} {
		if err := ValidateScreens(screens); err == nil || AsFault(err).Code != Invalid {
			t.Fatal("unbounded or ambiguous inventory was accepted", err)
		}
	}
}
