package pdfrender

import "testing"

func TestFitDPIKeepsRequestedWhenFits(t *testing.T) {
	// A4 при 200 dpi ≈ 1654×2339 ≈ 3.9 Мп — укладывается в 26 Мп.
	if got := FitDPI(1654, 2339, 200, 26_000_000); got != 200 {
		t.Errorf("FitDPI = %d, ожидалось 200", got)
	}
}

func TestFitDPIReducesOversizedPage(t *testing.T) {
	// A4 при 400 dpi ≈ 3308×4678 ≈ 15.5 Мп; при лимите 4 Мп DPI падает примерно вдвое.
	got := FitDPI(3308, 4678, 400, 4_000_000)

	if got >= 400 {
		t.Errorf("FitDPI = %d, ожидалось меньше 400", got)
	}
	if got < minDPI {
		t.Errorf("FitDPI = %d, ожидалось не меньше %d", got, minDPI)
	}
	// Проверяем инвариант по площади с допуском округления.
	pixels := int64(3308*400/400) * int64(4678*400/400)
	scaled := int64(float64(pixels) * float64(got) / 400 * float64(got) / 400)
	if scaled > 4_000_000*1.1 {
		t.Errorf("площадь при FitDPI=%d равна %d, ожидалось не более ~4.4 Мп", got, scaled)
	}
}

func TestFitDPIDoesNotGoBelowMinimum(t *testing.T) {
	if got := FitDPI(10000, 10000, 300, 1000); got != minDPI {
		t.Errorf("FitDPI = %d, ожидалось %d", got, minDPI)
	}
}

func TestFitDPIWithUnlimitedBudget(t *testing.T) {
	if got := FitDPI(5000, 5000, 150, 0); got != 150 {
		t.Errorf("FitDPI = %d, ожидалось 150", got)
	}
}

func TestFitDPIWithDegenerateInput(t *testing.T) {
	if got := FitDPI(0, 0, 200, 1000); got != 200 {
		t.Errorf("FitDPI при нулевых размерах = %d, ожидалось 200", got)
	}
	if got := FitDPI(100, 100, 0, 1000); got != 0 {
		t.Errorf("FitDPI при нулевом DPI = %d, ожидалось 0", got)
	}
}

func TestPixelsToPoints(t *testing.T) {
	page := Page{PointsPerPixel: 72.0 / 200.0}

	if got := page.PixelsToPoints(200); got != 72.0 {
		t.Errorf("PixelsToPoints(200) = %v, ожидалось 72", got)
	}

	// Без коэффициента координата возвращается как есть.
	plain := Page{}
	if got := plain.PixelsToPoints(42); got != 42 {
		t.Errorf("PixelsToPoints без коэффициента = %v, ожидалось 42", got)
	}
}