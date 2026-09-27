// Package spatial indexes object bounding boxes so the board can answer
// "what is in this rectangle" without scanning every object.
package spatial

import "math"

type Rect struct {
	X, Y, W, H float64
}

// Intersects includes touching edges, matching the web client.
func (r Rect) Intersects(o Rect) bool {
	return r.X <= o.X+o.W && o.X <= r.X+r.W && r.Y <= o.Y+o.H && o.Y <= r.Y+r.H
}

// Everything is a rectangle that intersects every finite one.
var Everything = Rect{X: -math.MaxFloat64 / 4, Y: -math.MaxFloat64 / 4, W: math.MaxFloat64 / 2, H: math.MaxFloat64 / 2}

type cellKey struct{ x, y int32 }

// Grid is a uniform grid: each rect is listed in every cell it covers.
// Moves are frequent on a whiteboard, and a grid update is O(cells covered),
// with no rebalancing. Rects covering more than maxCells cells go in a
// separate list that every query checks.
type Grid struct {
	cell     float64
	maxCells int
	cells    map[cellKey]map[string]struct{}
	rects    map[string]Rect
	large    map[string]struct{}
}

func NewGrid(cellSize float64, maxCells int) *Grid {
	return &Grid{
		cell:     cellSize,
		maxCells: maxCells,
		cells:    map[cellKey]map[string]struct{}{},
		rects:    map[string]Rect{},
		large:    map[string]struct{}{},
	}
}

func (g *Grid) Len() int { return len(g.rects) }

func (g *Grid) Get(id string) (Rect, bool) {
	r, ok := g.rects[id]
	return r, ok
}

// Set inserts or moves id.
func (g *Grid) Set(id string, r Rect) {
	if old, ok := g.rects[id]; ok {
		if old == r {
			return
		}
		g.unlink(id, old)
	}
	g.rects[id] = r
	x0, y0, x1, y1 := g.span(r)
	if cellCount(x0, y0, x1, y1) > float64(g.maxCells) {
		g.large[id] = struct{}{}
		return
	}
	for cx := x0; cx <= x1; cx++ {
		for cy := y0; cy <= y1; cy++ {
			k := cellKey{cx, cy}
			c := g.cells[k]
			if c == nil {
				c = map[string]struct{}{}
				g.cells[k] = c
			}
			c[id] = struct{}{}
		}
	}
}

func (g *Grid) Remove(id string) {
	if r, ok := g.rects[id]; ok {
		g.unlink(id, r)
		delete(g.rects, id)
	}
}

func (g *Grid) unlink(id string, r Rect) {
	if _, ok := g.large[id]; ok {
		delete(g.large, id)
		return
	}
	x0, y0, x1, y1 := g.span(r)
	for cx := x0; cx <= x1; cx++ {
		for cy := y0; cy <= y1; cy++ {
			k := cellKey{cx, cy}
			if c := g.cells[k]; c != nil {
				delete(c, id)
				if len(c) == 0 {
					delete(g.cells, k)
				}
			}
		}
	}
}

// Query calls fn once for every rect intersecting q, until fn returns false.
func (g *Grid) Query(q Rect, fn func(id string, r Rect) bool) {
	x0, y0, x1, y1 := g.span(q)
	// A query covering more cells than exist is cheaper as a full scan.
	if cellCount(x0, y0, x1, y1) > float64(len(g.cells)) {
		for id, r := range g.rects {
			if r.Intersects(q) && !fn(id, r) {
				return
			}
		}
		return
	}
	for id := range g.large {
		if r := g.rects[id]; r.Intersects(q) && !fn(id, r) {
			return
		}
	}
	for cx := x0; cx <= x1; cx++ {
		for cy := y0; cy <= y1; cy++ {
			for id := range g.cells[cellKey{cx, cy}] {
				r := g.rects[id]
				if !r.Intersects(q) {
					continue
				}
				// A rect spanning several cells is reported only from the cell
				// holding the top-left corner of its overlap with q.
				rx, ry := g.cellOf(math.Max(r.X, q.X)), g.cellOf(math.Max(r.Y, q.Y))
				if rx != cx || ry != cy {
					continue
				}
				if !fn(id, r) {
					return
				}
			}
		}
	}
}

func (g *Grid) cellOf(v float64) int32 {
	c := math.Floor(v / g.cell)
	return int32(math.Max(math.MinInt32, math.Min(math.MaxInt32, c)))
}

func (g *Grid) span(r Rect) (x0, y0, x1, y1 int32) {
	return g.cellOf(r.X), g.cellOf(r.Y), g.cellOf(r.X + r.W), g.cellOf(r.Y + r.H)
}

// cellCount is computed in float64: a huge rect spans ~2^32 cells per axis,
// and the product would overflow an int64.
func cellCount(x0, y0, x1, y1 int32) float64 {
	return (float64(x1) - float64(x0) + 1) * (float64(y1) - float64(y0) + 1)
}
