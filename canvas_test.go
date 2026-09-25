package main

import "testing"

func repeat(s Size, n int) []Size {
	out := make([]Size, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestChooseCanvas(t *testing.T) {
	cases := []struct {
		name    string
		pages   []Size
		maxEdge int
		want    Size
	}{
		{"uniform", repeat(Size{2400, 1350}, 41), 2560, Size{2400, 1350}},
		{"uniform portrait", repeat(Size{990, 1530}, 10), 2560, Size{990, 1530}},
		{
			// A deck whose widths vary at constant height: the modal page is
			// the widest, so nothing grows; the long edge is then capped.
			"varying widths, capped",
			append(append(append(append(
				repeat(Size{3795, 2025}, 58),
				Size{3514, 2025}),
				repeat(Size{3582, 2025}, 2)...),
				repeat(Size{3591, 2025}, 2)...),
				Size{3634, 2025}),
			2560, Size{2560, 1366},
		},
		{
			// A few fractionally larger outliers grow the canvas around them.
			"outliers grow canvas",
			append(append(repeat(Size{525, 704}, 228), repeat(Size{525, 706}, 2)...), repeat(Size{526, 706}, 2)...),
			2560, Size{526, 706},
		},
		{"no cap", repeat(Size{4000, 3000}, 3), 0, Size{4000, 3000}},
		{"odd rounded down to even", repeat(Size{1001, 1501}, 2), 0, Size{1000, 1500}},
		{
			"tie breaks toward larger area",
			[]Size{{800, 600}, {800, 600}, {1600, 1200}, {1600, 1200}},
			0, Size{1600, 1200},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chooseCanvas(c.pages, c.maxEdge); got != c.want {
				t.Errorf("chooseCanvas = %v, want %v", got, c.want)
			}
		})
	}
}

// Every page must fit inside the canvas without being shrunk.
func TestChooseCanvasContainsEveryPage(t *testing.T) {
	pages := []Size{{1000, 1400}, {1000, 1400}, {1000, 1400}, {1100, 1400}, {1000, 1500}}
	c := chooseCanvas(pages, 0)
	for _, p := range pages {
		if f := fitSize(p, c); f.W < p.W && f.H < p.H {
			t.Errorf("page %v would shrink to %v on canvas %v", p, f, c)
		}
	}
}

func TestBookOrientation(t *testing.T) {
	cases := []struct {
		canvas   Size
		override string
		mixed    bool
		want     Orientation
	}{
		{Size{2400, 1350}, "", false, Orientation{"landscape", "landscape", "landscape"}},
		{Size{990, 1530}, "", false, Orientation{"portrait", "portrait", "portrait"}},
		{Size{1000, 1000}, "", false, Orientation{"auto", "auto", "none"}},
		{Size{2400, 1350}, "", true, Orientation{"auto", "auto", "none"}},
		{Size{2400, 1350}, "portrait", false, Orientation{"portrait", "portrait", "portrait"}},
		{Size{2400, 1350}, "none", false, Orientation{"none", "auto", "none"}},
	}
	for _, c := range cases {
		if got := bookOrientation(c.canvas, c.override, c.mixed); got != c.want {
			t.Errorf("bookOrientation(%v, %q, %v) = %+v, want %+v", c.canvas, c.override, c.mixed, got, c.want)
		}
	}
}
