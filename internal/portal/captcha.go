package portal

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
)

// The portal's login captcha is four digits. Several preprocessing strategies
// and OCR page-segmentation modes are tried against the same image and the
// reading most of them agree on wins (deeds/utils/captcha_solver.py).

var psmModes = []string{"--psm 8", "--psm 7", "--psm 6"}

type variant struct {
	name string
	img  *image.Gray
}

func toGray(src image.Image) *image.Gray {
	b := src.Bounds()
	g := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, gg, bb, a := src.At(x, y).RGBA()
			// Composite on white like Pillow's convert("L") of an opaque image.
			r, gg, bb = r>>8, gg>>8, bb>>8
			if a != 0xffff {
				al := a >> 8
				r = (r*al + 255*(255-al)) / 255
				gg = (gg*al + 255*(255-al)) / 255
				bb = (bb*al + 255*(255-al)) / 255
			}
			l := (r*19595 + gg*38470 + bb*7471 + 0x8000) >> 16
			g.SetGray(x, y, color.Gray{Y: uint8(l)})
		}
	}
	return g
}

func clip(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v) // Pillow truncates
}

// enhance is ImageEnhance: blend(degenerate, image, factor) = degenerate + factor*(image - degenerate).
func enhance(img, degenerate *image.Gray, factor float64) *image.Gray {
	out := image.NewGray(img.Bounds())
	for i := range img.Pix {
		d, p := float64(degenerate.Pix[i]), float64(img.Pix[i])
		out.Pix[i] = clip(d + factor*(p-d))
	}
	return out
}

func meanGray(g *image.Gray) uint8 {
	var sum float64
	for _, p := range g.Pix {
		sum += float64(p)
	}
	if len(g.Pix) == 0 {
		return 0
	}
	return uint8(int(sum/float64(len(g.Pix)) + 0.5))
}

func contrast(g *image.Gray, factor float64) *image.Gray {
	deg := image.NewGray(g.Bounds())
	m := meanGray(g)
	for i := range deg.Pix {
		deg.Pix[i] = m
	}
	return enhance(g, deg, factor)
}

// smooth is ImageFilter.SMOOTH (kernel 1 1 1 / 1 5 1 / 1 1 1, scale 13); the
// one-pixel border is left as it was.
func smooth(g *image.Gray) *image.Gray {
	b := g.Bounds()
	out := image.NewGray(b)
	copy(out.Pix, g.Pix)
	w := b.Dx()
	for y := 1; y < b.Dy()-1; y++ {
		for x := 1; x < w-1; x++ {
			sum := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					v := int(g.Pix[(y+dy)*g.Stride+x+dx])
					if dx == 0 && dy == 0 {
						v *= 5
					}
					sum += v
				}
			}
			out.Pix[y*out.Stride+x] = uint8(float64(sum)/13 + 0.5)
		}
	}
	return out
}

func sharpen(g *image.Gray, factor float64) *image.Gray { return enhance(g, smooth(g), factor) }

// gaussianBlur blurs with a Gaussian of the given sigma.
func gaussianBlur(g *image.Gray, sigma float64) *image.Gray {
	radius := int(math.Ceil(sigma * 3))
	kernel := make([]float64, 2*radius+1)
	var total float64
	for i := range kernel {
		d := float64(i - radius)
		kernel[i] = math.Exp(-d * d / (2 * sigma * sigma))
		total += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= total
	}
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	tmp := make([]float64, w*h)
	at := func(x, y int) float64 {
		if x < 0 {
			x = 0
		}
		if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		}
		if y >= h {
			y = h - 1
		}
		return float64(g.Pix[y*g.Stride+x])
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for k, kv := range kernel {
				s += kv * at(x+k-radius, y)
			}
			tmp[y*w+x] = s
		}
	}
	out := image.NewGray(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			for k, kv := range kernel {
				yy := y + k - radius
				if yy < 0 {
					yy = 0
				}
				if yy >= h {
					yy = h - 1
				}
				s += kv * tmp[yy*w+x]
			}
			out.Pix[y*out.Stride+x] = uint8(s + 0.5)
		}
	}
	return out
}

func threshold(g *image.Gray, t uint8) *image.Gray {
	out := image.NewGray(g.Bounds())
	for i, p := range g.Pix {
		if p > t {
			out.Pix[i] = 255
		}
	}
	return out
}

func invert(g *image.Gray) *image.Gray {
	out := image.NewGray(g.Bounds())
	for i, p := range g.Pix {
		out.Pix[i] = 255 - p
	}
	return out
}

// preprocessingVariants are a handful of reasonably different preprocessed
// versions of the captcha image - each a different bet on what reads cleanly.
func preprocessingVariants(img image.Image) []variant {
	gray := toGray(img)
	contrasted := contrast(gray, 2)
	thr := threshold(gray, 140)
	return []variant{
		{"grayscale", gray},
		{"contrast x2", contrasted},
		{"contrast x2 + blur", gaussianBlur(contrasted, 1)},
		{"sharpened", sharpen(gray, 2)},
		{"threshold@140", thr},
		{"threshold@140 inverted", invert(thr)},
	}
}

var nonDigit = regexp.MustCompile(`\D`)

// Tesseract reads text from an image with the tesseract command line tool.
type Tesseract struct{ Cmd string }

func (t Tesseract) read(ctx context.Context, img *image.Gray, psm string) (string, error) {
	f, err := os.CreateTemp("", "captcha-*.png")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	cmd := exec.CommandContext(ctx, t.Cmd, f.Name(), "stdout", "--oem", "3")
	cmd.Args = append(cmd.Args, strings.Fields(psm)...)
	cmd.Args = append(cmd.Args, "-c", "tessedit_char_whitelist=0123456789")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// SolveCaptcha tries several preprocessing/OCR-mode combinations against one
// captcha image. It returns the digits ("" if unsolved) and a log of what
// every attempt read, for diagnosing a captcha that won't solve.
func (t Tesseract) SolveCaptcha(ctx context.Context, imageBytes []byte, expectedLength int) (string, []string) {
	img, _, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return "", []string{fmt.Sprintf("could not decode the captcha image: %v", err)}
	}
	var log []string
	votes := map[string]int{}
	var order []string
	for _, v := range preprocessingVariants(img) {
		for _, psm := range psmModes {
			raw, err := t.read(ctx, v.img, psm)
			if err != nil {
				log = append(log, fmt.Sprintf("[%s, %s] OCR error: %v", v.name, psm, err))
				continue
			}
			cleaned := nonDigit.ReplaceAllString(raw, "")
			log = append(log, fmt.Sprintf("[%s, %s] read %q -> %q", v.name, psm, strings.TrimSpace(raw), cleaned))
			if len(cleaned) == expectedLength {
				if votes[cleaned] == 0 {
					order = append(order, cleaned)
				}
				votes[cleaned]++
			}
		}
	}
	// Every combination gets a say and the most common reading wins: the first
	// plausible one alone was wrong on 2 of 8 live logins. Ties: the earliest.
	best, bestVotes := "", 0
	for _, c := range order {
		if votes[c] > bestVotes {
			best, bestVotes = c, votes[c]
		}
	}
	if best != "" {
		slog.Info("Captcha solved", "answer", best, "votes", votes)
		return best, log
	}
	slog.Warn("Captcha not solved", "attempts", len(log), "log", log)
	return "", log
}
