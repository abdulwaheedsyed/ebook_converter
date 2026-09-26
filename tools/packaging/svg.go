package main

// A renderer for the small subset of SVG that Leafbind's icon uses: rect with
// rounded corners, path with fill or round-capped stroke, and a two-stop
// linear gradient. Anything else is an error rather than a wrong picture.

import (
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/image/vector"
)

type pt struct{ x, y float64 }

// shape is a set of closed polygons in icon units, with how to paint them.
type shape struct {
	polys [][]pt
	paint func(bounds [4]float64) image.Image // given the shape's bounding box
}

// icon is a parsed SVG: a view box and shapes in painting order.
type icon struct {
	w, h   float64
	shapes []shape
}

type gradient struct {
	x1, y1, x2, y2 float64
	stops          []color.NRGBA // at 0 and 1
}

func parseIcon(r io.Reader) (*icon, error) {
	d := xml.NewDecoder(r)
	ic := &icon{}
	grads := map[string]*gradient{}
	var grad *gradient
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		a := map[string]string{}
		for _, at := range se.Attr {
			a[at.Name.Local] = at.Value
		}
		switch se.Name.Local {
		case "svg":
			f := strings.Fields(a["viewBox"])
			if len(f) != 4 {
				return nil, fmt.Errorf("svg needs a viewBox")
			}
			ic.w, ic.h = num(f[2]), num(f[3])
		case "defs", "title", "desc":
		case "linearGradient":
			grad = &gradient{x1: num(a["x1"]), y1: num(a["y1"]), x2: num(a["x2"]), y2: num(a["y2"])}
			grads[a["id"]] = grad
		case "stop":
			c, err := parseColor(a["stop-color"])
			if err != nil {
				return nil, err
			}
			grad.stops = append(grad.stops, c)
		case "rect":
			w, h, rx := num(a["width"]), num(a["height"]), num(a["rx"])
			x, y := num(a["x"]), num(a["y"])
			paint, err := paintFor(a["fill"], grads)
			if err != nil {
				return nil, err
			}
			ic.shapes = append(ic.shapes, shape{polys: [][]pt{roundRect(x, y, w, h, rx)}, paint: paint})
		case "path":
			subs, err := parsePath(a["d"])
			if err != nil {
				return nil, err
			}
			if s := a["stroke"]; s != "" && s != "none" {
				if a["stroke-linecap"] != "round" {
					return nil, fmt.Errorf("only round stroke caps are supported")
				}
				paint, err := paintFor(s, grads)
				if err != nil {
					return nil, err
				}
				ic.shapes = append(ic.shapes, shape{polys: strokePolys(subs, num(a["stroke-width"])/2), paint: paint})
			}
			if f, ok := a["fill"]; !ok || f != "none" {
				if !ok {
					f = "#000000"
				}
				paint, err := paintFor(f, grads)
				if err != nil {
					return nil, err
				}
				ic.shapes = append(ic.shapes, shape{polys: subs, paint: paint})
			}
		default:
			return nil, fmt.Errorf("unsupported SVG element <%s>", se.Name.Local)
		}
	}
	if ic.w == 0 || ic.h == 0 {
		return nil, fmt.Errorf("no svg element")
	}
	return ic, nil
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func parseColor(s string) (color.NRGBA, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return color.NRGBA{}, fmt.Errorf("unsupported colour %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, err
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}

func paintFor(fill string, grads map[string]*gradient) (func([4]float64) image.Image, error) {
	if id, ok := strings.CutPrefix(fill, "url(#"); ok {
		g := grads[strings.TrimSuffix(id, ")")]
		if g == nil || len(g.stops) != 2 {
			return nil, fmt.Errorf("gradient %s needs two stops", fill)
		}
		return func(b [4]float64) image.Image { return &gradientImage{g: g, box: b} }, nil
	}
	c, err := parseColor(fill)
	if err != nil {
		return nil, err
	}
	return func([4]float64) image.Image { return image.NewUniform(c) }, nil
}

// gradientImage paints a linear gradient in objectBoundingBox units, in
// device pixels once render has scaled the box.
type gradientImage struct {
	g   *gradient
	box [4]float64 // x0, y0, x1, y1 in pixels
}

func (gi *gradientImage) ColorModel() color.Model { return color.NRGBAModel }
func (gi *gradientImage) Bounds() image.Rectangle {
	return image.Rect(-1e6, -1e6, 1e6, 1e6)
}
func (gi *gradientImage) At(x, y int) color.Color {
	bw, bh := gi.box[2]-gi.box[0], gi.box[3]-gi.box[1]
	u := (float64(x) + .5 - gi.box[0]) / bw
	v := (float64(y) + .5 - gi.box[1]) / bh
	dx, dy := gi.g.x2-gi.g.x1, gi.g.y2-gi.g.y1
	t := ((u-gi.g.x1)*dx + (v-gi.g.y1)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	a, b := gi.g.stops[0], gi.g.stops[1]
	mix := func(p, q uint8) uint8 { return uint8(math.Round(float64(p) + (float64(q)-float64(p))*t)) }
	return color.NRGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 255}
}

// render draws the icon into a size x size image, scaled by scale and
// centred; scale 1 fills the image.
func (ic *icon) render(size int, scale float64) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	k := float64(size) / math.Max(ic.w, ic.h) * scale
	off := (float64(size) - ic.w*k) / 2
	for _, s := range ic.shapes {
		r := vector.NewRasterizer(size, size)
		b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, poly := range s.polys {
			for i, p := range poly {
				x, y := float32(off+p.x*k), float32(off+p.y*k)
				if i == 0 {
					r.MoveTo(x, y)
				} else {
					r.LineTo(x, y)
				}
				b = [4]float64{math.Min(b[0], float64(x)), math.Min(b[1], float64(y)), math.Max(b[2], float64(x)), math.Max(b[3], float64(y))}
			}
			r.ClosePath()
		}
		r.DrawOp = draw.Over
		r.Draw(dst, dst.Bounds(), s.paint(b), image.Point{})
	}
	return dst
}

// ----- geometry -----

// kappa places cubic control points to approximate a quarter circle.
const kappa = 0.5522847498

func cubic(p0, p1, p2, p3 pt) []pt {
	const n = 24
	out := make([]pt, 0, n)
	for i := 1; i <= n; i++ {
		t := float64(i) / n
		u := 1 - t
		out = append(out, pt{
			u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
			u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
		})
	}
	return out
}

func roundRect(x, y, w, h, r float64) []pt {
	r = math.Min(r, math.Min(w, h)/2)
	c := r * kappa
	poly := []pt{{x + r, y}, {x + w - r, y}}
	poly = append(poly, cubic(pt{x + w - r, y}, pt{x + w - r + c, y}, pt{x + w, y + r - c}, pt{x + w, y + r})...)
	poly = append(poly, pt{x + w, y + h - r})
	poly = append(poly, cubic(pt{x + w, y + h - r}, pt{x + w, y + h - r + c}, pt{x + w - r + c, y + h}, pt{x + w - r, y + h})...)
	poly = append(poly, pt{x + r, y + h})
	poly = append(poly, cubic(pt{x + r, y + h}, pt{x + r - c, y + h}, pt{x, y + h - r + c}, pt{x, y + h - r})...)
	poly = append(poly, pt{x, y + r})
	poly = append(poly, cubic(pt{x, y + r}, pt{x, y + r - c}, pt{x + r - c, y}, pt{x + r, y})...)
	return poly
}

func circle(c pt, r float64) []pt {
	const n = 32
	out := make([]pt, n)
	for i := range out {
		a := 2 * math.Pi * float64(i) / n
		out[i] = pt{c.x + r*math.Cos(a), c.y + r*math.Sin(a)}
	}
	return out
}

// strokePolys outlines polylines with round caps and joins: a disc at every
// vertex and a rectangle along every segment. The rasterizer adds up
// coverage, so every polygon is given the same winding to make them a union.
func strokePolys(lines [][]pt, hw float64) [][]pt {
	var out [][]pt
	for _, l := range lines {
		for i, p := range l {
			out = append(out, circle(p, hw))
			if i == 0 {
				continue
			}
			q := l[i-1]
			dx, dy := p.x-q.x, p.y-q.y
			n := math.Hypot(dx, dy)
			if n == 0 {
				continue
			}
			nx, ny := -dy/n*hw, dx/n*hw
			out = append(out, []pt{{q.x + nx, q.y + ny}, {p.x + nx, p.y + ny}, {p.x - nx, p.y - ny}, {q.x - nx, q.y - ny}})
		}
	}
	for i, p := range out {
		if signedArea(p) < 0 {
			for a, b := 0, len(p)-1; a < b; a, b = a+1, b-1 {
				p[a], p[b] = p[b], p[a]
			}
			out[i] = p
		}
	}
	return out
}

func signedArea(p []pt) float64 {
	var a float64
	for i := range p {
		j := (i + 1) % len(p)
		a += p[i].x*p[j].y - p[j].x*p[i].y
	}
	return a / 2
}

var pathToken = regexp.MustCompile(`[MmLlHhVvCcSsZz]|[-+]?(?:\d*\.\d+|\d+\.?)(?:[eE][-+]?\d+)?`)

// parsePath flattens path data into polylines, one per subpath. Only the
// commands the icon uses are supported.
func parsePath(d string) ([][]pt, error) {
	toks := pathToken.FindAllString(d, -1)
	var subs [][]pt
	var cur []pt
	var pos, start, lastCtrl pt
	var cmd byte
	i := 0
	next := func() float64 { v := num(toks[i]); i++; return v }
	isNum := func() bool { return i < len(toks) && !strings.ContainsAny(toks[i][:1], "MmLlHhVvCcSsZz") }
	flush := func() {
		if len(cur) > 1 {
			subs = append(subs, cur)
		}
		cur = nil
	}
	for i < len(toks) {
		if !isNum() {
			cmd = toks[i][0]
			i++
		}
		rel := cmd >= 'a'
		at := func(x, y float64) pt {
			if rel {
				return pt{pos.x + x, pos.y + y}
			}
			return pt{x, y}
		}
		switch cmd | 0x20 {
		case 'm':
			flush()
			pos = at(next(), next())
			start, lastCtrl = pos, pos
			cur = []pt{pos}
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			pos = at(next(), next())
			cur = append(cur, pos)
			lastCtrl = pos
		case 'h':
			x := next()
			if rel {
				pos.x += x
			} else {
				pos.x = x
			}
			cur, lastCtrl = append(cur, pos), pos
		case 'v':
			y := next()
			if rel {
				pos.y += y
			} else {
				pos.y = y
			}
			cur, lastCtrl = append(cur, pos), pos
		case 'c':
			c1 := at(next(), next())
			c2 := at(next(), next())
			p := at(next(), next())
			cur = append(cur, cubic(pos, c1, c2, p)...)
			pos, lastCtrl = p, c2
		case 's':
			c1 := pt{2*pos.x - lastCtrl.x, 2*pos.y - lastCtrl.y}
			c2 := at(next(), next())
			p := at(next(), next())
			cur = append(cur, cubic(pos, c1, c2, p)...)
			pos, lastCtrl = p, c2
		case 'z':
			pos = start
			flush()
			cur = []pt{pos}
		default:
			return nil, fmt.Errorf("unsupported path command %q", cmd)
		}
	}
	flush()
	return subs, nil
}
