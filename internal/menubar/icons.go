package menubar

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"

	"github.com/eltupe/lagra/internal/engine"
)

// The menu bar icon is an anchor, drawn at runtime as a black
// template image with alpha so macOS recolours it for light and dark menu
// bars. 44×44 pixels is the Retina size of a 22-point status item. The
// overall state is a small badge in the lower right: none when everything
// is connected, a dot while something is connecting or offline, an
// exclamation mark on errors; the whole anchor is dimmed when nothing is
// connected at all.
const iconSize = 44

var icons = sync.OnceValue(func() map[string][]byte {
	return map[string][]byte{
		"ok":      render(1, badgeNone),
		"idle":    render(0.55, badgeNone),
		"partial": render(1, badgeDot),
		"error":   render(1, badgeBang),
	}
})

func icon(kind string) []byte { return icons()[kind] }

// iconFor maps an overall state (and config validity) to an icon.
func iconFor(overall engine.Overall, configOK bool) []byte {
	if !configOK {
		return icon("error")
	}
	switch overall {
	case engine.OverallOK:
		return icon("ok")
	case engine.OverallPartial:
		return icon("partial")
	case engine.OverallError:
		return icon("error")
	}
	return icon("idle")
}

type badge int

const (
	badgeNone badge = iota
	badgeDot
	badgeBang
)

type pt struct{ x, y float64 }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// stroke coverage for a point at distance d from a curve of the given width.
func cover(d, width float64) float64 { return clamp(width/2-d+0.5, 0, 1) }

func distSeg(p, a, b pt) float64 {
	abx, aby := b.x-a.x, b.y-a.y
	apx, apy := p.x-a.x, p.y-a.y
	t := clamp((apx*abx+apy*aby)/(abx*abx+aby*aby), 0, 1)
	dx, dy := apx-t*abx, apy-t*aby
	return math.Hypot(dx, dy)
}

func distCircle(p, c pt, r float64) float64 { return math.Abs(math.Hypot(p.x-c.x, p.y-c.y) - r) }

// distArc measures the distance to the part of a circle between angles a0
// and a1 (radians, clockwise from +x with y pointing down).
func distArc(p, c pt, r, a0, a1 float64) float64 {
	ang := math.Atan2(p.y-c.y, p.x-c.x)
	if ang < 0 {
		ang += 2 * math.Pi
	}
	if ang >= a0 && ang <= a1 {
		return distCircle(p, c, r)
	}
	e0 := pt{c.x + r*math.Cos(a0), c.y + r*math.Sin(a0)}
	e1 := pt{c.x + r*math.Cos(a1), c.y + r*math.Sin(a1)}
	return math.Min(math.Hypot(p.x-e0.x, p.y-e0.y), math.Hypot(p.x-e1.x, p.y-e1.y))
}

// distTri is a signed-ish distance to a filled triangle (0 inside).
func distTri(p, a, b, c pt) float64 {
	inside := func(p, a, b, c pt) bool {
		s1 := (b.x-a.x)*(p.y-a.y) - (b.y-a.y)*(p.x-a.x)
		s2 := (c.x-b.x)*(p.y-b.y) - (c.y-b.y)*(p.x-b.x)
		s3 := (a.x-c.x)*(p.y-c.y) - (a.y-c.y)*(p.x-c.x)
		return (s1 >= 0 && s2 >= 0 && s3 >= 0) || (s1 <= 0 && s2 <= 0 && s3 <= 0)
	}
	if inside(p, a, b, c) {
		return 0
	}
	return math.Min(distSeg(p, a, b), math.Min(distSeg(p, b, c), distSeg(p, c, a)))
}

// anchorAlpha is the anchor glyph's coverage at p. The glyph uses the whole
// canvas because the status item shows it at 16 points; strokes are heavy
// so it holds its own next to filled system icons. Coordinates are in the
// 44-unit menu bar grid; px is the size of one output pixel in that grid
// (1 for the menu bar), so edges stay one pixel soft at any scale.
func anchorAlpha(p pt, px float64) float64 {
	const cx = iconSize / 2.0
	a := 0.0
	paint := func(v float64) { a = max(a, v) }
	cover := func(d, width float64) float64 { return clamp((width/2-d)/px+0.5, 0, 1) }
	const w = 5.2 // stroke width
	// ring at the top
	paint(cover(distCircle(p, pt{cx, 7.6}, 5.0), 4.4))
	// stem; its round cap ends where the ring's hole begins
	paint(cover(distSeg(p, pt{cx, 13.0}, pt{cx, 40.0}), w))
	// crossbar
	paint(cover(distSeg(p, pt{cx - 9.5, 18.6}, pt{cx + 9.5, 18.6}), 4.6))
	// arms: lower arc, with filled flukes at the ends. The arc ends flat a
	// little short of the flukes' anchor points so no cap shows past them.
	c := pt{cx, 24.8}
	const r = 15.4
	a0, a1 := 18*math.Pi/180, 162*math.Pi/180
	const tuck = 3 * math.Pi / 180
	in0 := (p.y-c.y)*math.Cos(a0+tuck) - (p.x-c.x)*math.Sin(a0+tuck)
	in1 := (p.x-c.x)*math.Sin(a1-tuck) - (p.y-c.y)*math.Cos(a1-tuck)
	paint(cover(distCircle(p, c, r), w) * clamp(in0/px+0.5, 0, 1) * clamp(in1/px+0.5, 0, 1))
	for _, s := range []float64{1, -1} {
		end := pt{c.x + s*r*math.Cos(a0), c.y + r*math.Sin(a0)}
		// fluke: triangle pointing up and outward
		t := distTri(p, pt{end.x - s*3.4, end.y + 2.2}, pt{end.x + s*3.2, end.y + 1.4}, pt{end.x + s*0.6, end.y - 7.2})
		paint(clamp(1-t/px, 0, 1))
	}
	return a
}

func render(alpha float64, b badge) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	bc := pt{34.5, 34.5}
	const br = 8.6
	// With a badge the anchor shrinks towards the top left so nothing of it
	// peeks out from behind the badge.
	scale, off := 1.0, 0.0
	if b != badgeNone {
		scale, off = 0.8, -0.5
	}
	for y := range iconSize {
		for x := range iconSize {
			p := pt{float64(x) + 0.5, float64(y) + 0.5}
			q := pt{(p.x - off) / scale, (p.y - off) / scale}
			a := anchorAlpha(q, 1) * alpha
			if b != badgeNone {
				d := math.Hypot(p.x-bc.x, p.y-bc.y)
				// transparent gap around the badge, anti-aliased
				a *= clamp(d-(br+2.4)+0.5, 0, 1)
				body := clamp(br-d+0.5, 0, 1)
				knock := 0.0
				switch b {
				case badgeDot:
					knock = cover(d, 5.6)
				case badgeBang:
					knock = math.Max(cover(distSeg(p, pt{bc.x, bc.y - 4.6}, pt{bc.x, bc.y + 0.8}), 3.0), cover(math.Hypot(p.x-bc.x, p.y-(bc.y+4.6)), 3.0))
				}
				a = math.Max(a, body*(1-knock))
			}
			if a <= 0.002 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, uint8(clamp(a, 0, 1)*255 + 0.5)})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
