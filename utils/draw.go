package utils

import (
	"image/color"
	"log"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
)

func Draw(title, filePath string, seqs []uint64) {
	p := plot.New()

	p.Title.Text = title
	p.X.Label.Text = "sequence"
	p.Y.Label.Text = "time elapsed(ms)"

	pts := make(plotter.XYs, len(seqs))

	for i, seq := range seqs {
		pts[i] = plotter.XY{X: float64(i), Y: float64(seq)}
	}

	s, err := plotter.NewScatter(pts)
	if err != nil {
		log.Fatalf("Cannot create scatter: %s", err)
	}
	// Set the dot color to black
	s.GlyphStyle.Color = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	// Set the dot shape to be a filled circle
	s.GlyphStyle.Shape = draw.CircleGlyph{}

	p.Add(s)

	if err := p.Save(16*vg.Inch, 9*vg.Inch, filePath+".png"); err != nil {
		log.Fatalf("Cannot save: %s", err)
	}
}
