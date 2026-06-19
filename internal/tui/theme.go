package tui

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name   string
	Bg     lipgloss.Color
	Fg     lipgloss.Color
	Muted  lipgloss.Color
	Accent lipgloss.Color
	Good   lipgloss.Color
	Warn   lipgloss.Color
	Bad    lipgloss.Color
	Panel  lipgloss.Color
	BarBg  lipgloss.Color
}

func DefaultTheme() Theme {
	return BlulocoDarkTheme()
}

func BuiltInThemes() []Theme {
	return []Theme{
		BlulocoDarkTheme(),
		BlulocoLightTheme(),
	}
}

func BlulocoDarkTheme() Theme {
	return Theme{
		Name:   "Bluloco Dark",
		Bg:     lipgloss.Color("#282c34"),
		Fg:     lipgloss.Color("#b9c0cb"),
		Muted:  lipgloss.Color("#8f9aae"),
		Accent: lipgloss.Color("#3476ff"),
		Good:   lipgloss.Color("#25a45c"),
		Warn:   lipgloss.Color("#ff936a"),
		Bad:    lipgloss.Color("#fc2f52"),
		Panel:  lipgloss.Color("#373e4d"),
		BarBg:  lipgloss.Color("#4b5263"),
	}
}

func BlulocoLightTheme() Theme {
	return Theme{
		Name:   "Bluloco Light",
		Bg:     lipgloss.Color("#f9f9f9"),
		Fg:     lipgloss.Color("#373a41"),
		Muted:  lipgloss.Color("#676a77"),
		Accent: lipgloss.Color("#275fe4"),
		Good:   lipgloss.Color("#23974a"),
		Warn:   lipgloss.Color("#df631c"),
		Bad:    lipgloss.Color("#d52753"),
		Panel:  lipgloss.Color("#d0e1f9"),
		BarBg:  lipgloss.Color("#b0c4de"),
	}
}

type styles struct {
	title     lipgloss.Style
	subtle    lipgloss.Style
	panel     lipgloss.Style
	panelGood lipgloss.Style
	panelWarn lipgloss.Style
	panelBad  lipgloss.Style
	heading   lipgloss.Style
	good      lipgloss.Style
	warn      lipgloss.Style
	bad       lipgloss.Style
	provider  lipgloss.Style
	barBg     lipgloss.Style
}

func newStyles(theme Theme) styles {
	panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Panel).Padding(0, 1)
	return styles{
		title:     lipgloss.NewStyle().Foreground(theme.Accent).Bold(true),
		subtle:    lipgloss.NewStyle().Foreground(theme.Muted),
		panel:     panel,
		panelGood: panel.BorderForeground(theme.Good),
		panelWarn: panel.BorderForeground(theme.Warn),
		panelBad:  panel.BorderForeground(theme.Bad),
		heading:   lipgloss.NewStyle().Foreground(theme.Fg).Bold(true),
		good:      lipgloss.NewStyle().Foreground(theme.Good).Bold(true),
		warn:      lipgloss.NewStyle().Foreground(theme.Warn).Bold(true),
		bad:       lipgloss.NewStyle().Foreground(theme.Bad).Bold(true),
		provider:  lipgloss.NewStyle().Foreground(theme.Accent).Bold(true),
		barBg:     lipgloss.NewStyle().Foreground(theme.BarBg),
	}
}
