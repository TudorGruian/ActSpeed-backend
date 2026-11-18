package scheme

import (
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

// Cell is one selectable grid cell.
type Cell struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Param int    `json:"param,omitempty"`
}

// Grid is a scanning keyboard: the stick moves a cursor over cells, the
// counterclockwise circle (or resting on a cell when dwell is enabled) selects. Page 0 is
// letters with a prediction row on top, page 1 is the phrase bank.
type Grid struct {
	dwellMs     int
	phrases     []string
	predictions []string

	page, row, col int
	lastMove       int64
	dwellArmed     bool
}

const predictionCells = 3

var letterRows = [][]string{
	{"e", "t", "a", "o", "i", "n"},
	{"s", "h", "r", "d", "l", "c"},
	{"u", "m", "w", "f", "g", "y"},
	{"p", "b", "v", "k", "j", "x"},
	{"q", "z", ".", "?", "'", ","},
}

// NewGrid builds a grid from profile options.
func NewGrid(o Options) *Grid {
	return &Grid{dwellMs: o.DwellSelectMs, phrases: o.Phrases, row: 1}
}

func (*Grid) Name() string                         { return model.SchemeGrid }
func (*Grid) NormalizeGesture(x []string) []string { return x }

func ctl(label, value string) Cell { return Cell{Label: label, Kind: model.OutControl, Value: value} }

// Cells returns the rows of the current page.
func (g *Grid) Cells() [][]Cell {
	if g.page == 1 {
		var rows [][]Cell
		for i := 0; i < len(g.phrases); i += 2 {
			row := []Cell{{Label: g.phrases[i], Kind: model.OutPhrase, Value: g.phrases[i]}}
			if i+1 < len(g.phrases) {
				row = append(row, Cell{Label: g.phrases[i+1], Kind: model.OutPhrase, Value: g.phrases[i+1]})
			}
			rows = append(rows, row)
		}
		return append(rows, []Cell{ctl("letters", model.CtlGridNextPage)})
	}
	pred := make([]Cell, predictionCells)
	for i := range pred {
		pred[i] = Cell{Kind: model.OutControl, Value: model.CtlTextAcceptPredic, Param: i}
		if i < len(g.predictions) {
			pred[i].Label = g.predictions[i]
		}
	}
	rows := [][]Cell{pred}
	for _, lr := range letterRows {
		row := make([]Cell, len(lr))
		for i, ch := range lr {
			row[i] = Cell{Label: ch, Kind: model.OutChar, Value: ch}
		}
		rows = append(rows, row)
	}
	return append(rows, []Cell{
		ctl("space", model.CtlTextSpace),
		ctl("delete", model.CtlTextBackspace),
		ctl("clear", model.CtlTextClear),
		ctl("speak", model.CtlTextSpeak),
		ctl("phrases", model.CtlGridNextPage),
	})
}

func (g *Grid) clamp(rows [][]Cell) {
	if g.row >= len(rows) {
		g.row = len(rows) - 1
	}
	if g.col >= len(rows[g.row]) {
		g.col = len(rows[g.row]) - 1
	}
}

func (g *Grid) move(d signal.Direction, now int64) {
	rows := g.Cells()
	switch d {
	case signal.N:
		g.row = (g.row - 1 + len(rows)) % len(rows)
	case signal.S:
		g.row = (g.row + 1) % len(rows)
	}
	g.clamp(rows)
	n := len(rows[g.row])
	switch d {
	case signal.W:
		g.col = (g.col - 1 + n) % n
	case signal.E:
		g.col = (g.col + 1) % n
	}
	g.lastMove, g.dwellArmed = now, true
}

// Select activates the currently highlighted cell.
func (g *Grid) Select() []Output {
	rows := g.Cells()
	g.clamp(rows)
	c := rows[g.row][g.col]
	g.dwellArmed = false
	if c.Kind == model.OutControl && c.Value == model.CtlGridNextPage {
		g.page = 1 - g.page
		g.row, g.col = 0, 0
		if g.page == 0 {
			g.row = 1
		}
		return []Output{{Kind: model.OutControl, Value: model.CtlGridNextPage}}
	}
	if c.Kind == model.OutControl && c.Value == model.CtlTextAcceptPredic && c.Label == "" {
		return nil
	}
	return []Output{{Kind: c.Kind, Value: c.Value, Param: c.Param}}
}

func (g *Grid) Token(t signal.Token) ([]Output, bool) {
	switch t.Kind {
	case signal.KindMove, signal.KindRepeat:
		g.move(t.Dir, t.T)
		// A move changes what the client must draw, so report it.
		return []Output{{Kind: "state"}}, true
	case signal.KindHold:
		return nil, true
	}
	return nil, false
}

func (g *Grid) Tick(now int64) []Output {
	if g.dwellMs <= 0 || !g.dwellArmed || now-g.lastMove < int64(g.dwellMs) {
		return nil
	}
	return g.Select()
}

func (g *Grid) SetPredictions(p []string) { g.predictions = p }

func (g *Grid) State() any {
	rows := g.Cells()
	g.clamp(rows)
	return map[string]any{"page": g.page, "row": g.row, "col": g.col, "cells": rows}
}
