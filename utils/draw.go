package utils

import (
	"image/color"
	"log"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
)

func Draw(title, filePath string, seqs []uint64, p25, p50, p75, p90 float64) {
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

	p25Line := plotter.NewFunction(func(x float64) float64 { return p25 })
	p50Line := plotter.NewFunction(func(x float64) float64 { return p50 })
	p75Line := plotter.NewFunction(func(x float64) float64 { return p75 })
	p90Line := plotter.NewFunction(func(x float64) float64 { return p90 })

	// 设置横线的颜色
	p25Line.Color = color.RGBA{R: 92, G: 102, B: 172, A: 255}  // 红色
	p50Line.Color = color.RGBA{R: 158, G: 192, B: 119, A: 255} // 绿色
	p75Line.Color = color.RGBA{R: 191, G: 104, B: 92, A: 255}  // 蓝色
	p90Line.Color = color.RGBA{R: 228, G: 199, B: 101, A: 255} // 蓝色

	// 将横线添加到主绘图中
	p.Add(p25Line, p50Line, p75Line)
	p.Legend.Add("p25", p25Line)
	p.Legend.Add("p50", p50Line)
	p.Legend.Add("p75", p75Line)
	p.Legend.Add("p90", p90Line)

	p.Legend.Top = true

	if err := p.Save(16*vg.Inch, 9*vg.Inch, filePath+".png"); err != nil {
		log.Fatalf("Cannot save: %s", err)
	}
}
