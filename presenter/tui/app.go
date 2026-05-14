// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package tui provides the interactive terminal-UI presenter for techne.
//
// The presenter is currently a stub. The intended design is a bubbletea
// application with the standard Model/Update/View triad: a top-level
// Model holds the registered [tool.Tool] slice and the active screen
// (picker, form, or result); Update dispatches keyboard and message
// events; View renders the current screen via lipgloss styling. Tool
// inputs are collected through huh forms whose fields are derived from
// each tool's JSON input schema at runtime, and a bubbles list provides
// the searchable tool picker. Output is rendered as syntax-highlighted
// JSON in a scrollable pane.
//
// The package is wired into the binary via 'techne tui'; see [App] for
// the entry point.
package tui

import (
	"context"
	"errors"

	"go.thesmos.sh/techne/internal/tool"
)

// App is the terminal-UI presenter for techne. It is a stub today: the
// intended implementation follows the bubbletea Model/Update/View pattern,
// with a huh-based form builder for tool inputs, a bubbles list for tool
// selection, and lipgloss styling for output rendering. The Model wraps
// the registered tools so the view layer can browse them by group; Update
// dispatches keyboard and message events into either the picker or the
// active form; View renders the current screen — picker, form, or
// result — framed by a status bar.
//
// App is constructed with [NewApp] and driven by [App.Run]. Until the
// bubbletea wiring lands, Run returns 'not implemented'.
type App struct {
	tools []tool.Tool
}

// NewApp constructs a TUI [App] over the given tool set. The tools are
// retained on the App so the eventual bubbletea Model can present them as
// a searchable list and derive form fields from each tool's InputSchema
// on demand.
//
// NewApp is cheap and side-effect free; nothing is rendered until
// [App.Run] is called. The tools slice is borrowed, not copied — callers
// should not mutate it for the lifetime of the App.
func NewApp(tools []tool.Tool) *App {
	return &App{tools: tools}
}

// Run starts the TUI event loop and blocks until the user quits or ctx is
// cancelled. The eventual implementation will:
//
//   - install bubbletea's alternate-screen and raw-mode handlers,
//   - render the tool picker as the initial view,
//   - on selection, derive a huh form from the chosen tool's input
//     schema, collect inputs, marshal them to JSON, and call the tool's
//     Execute method,
//   - display the JSON-marshalled result (or any error) in a scrollable
//     pane until the user dismisses it,
//   - return nil on a clean exit, or a non-nil error if the terminal
//     transport itself failed.
//
// Currently Run is unimplemented and returns a stub error. The signature
// is stable: the ctx is honoured exactly as documented above once the
// implementation lands.
func (*App) Run(ctx context.Context) error {
	return errors.New("not implemented")
}
