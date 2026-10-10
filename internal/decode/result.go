package decode

import (
	"image"
	"sort"

	"barcodereader/internal/scan"
)

// Источник находки.
const (
	SourceEmbeddedImage = "embedded_image"
	SourceRenderedPage  = "rendered_page"
)

// BBox — прямоугольник в пунктах PDF (для отрендеренной страницы) или
// в пикселях исходной картинки (для встроенного изображения).
type BBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Code — одна найденная запись в codes[].
type Code struct {
	Value  string
	Kind   string // "qr" | "barcode"
	Format string // "QR_CODE", "EAN_13", ...
	Page   int    // номер страницы, начиная с 1
	Source string // SourceEmbeddedImage | SourceRenderedPage
	// ImageObject заполняется только для встроенных изображений.
	ImageObject int
	// BBox заполняется, когда удалось вычислить положение кода.
	BBox *BBox
	// Points — координаты в единицах источника; для штрихкодов это
	// всего две точки (концы кода).
	Points []image.Point

	// order — порядок обнаружения; используется как ключ сортировки,
	// чтобы результат был детерминированным.
	order int
}

// Meta — служебная информация о разборе документа.
type Meta struct {
	PagesTotal          int      `json:"pages_total"`
	PagesScanned        int      `json:"pages_scanned"`
	DPI                 int      `json:"dpi"`
	StagesUsed          []string `json:"stages_used"`
	CodesTotal          int      `json:"codes_total"`
	CodesUnique         int      `json:"codes_unique"`
	DuplicatesCollapsed int      `json:"duplicates_collapsed"`
	ElapsedMS           int64    `json:"elapsed_ms"`
}

// Result — итог декодирования документа.
type Result struct {
	Found bool
	Value string
	Codes []Code
	Meta  Meta
}

// aggregate сортирует находки и сворачивает одинаковые значения.
//
// Сортировка идёт по странице, затем по порядку обнаружения (стадия A раньше
// стадии B). Свёртка выполняется после сортировки, поэтому из нескольких
// одинаковых значений остаётся вхождение с наименьшим номером страницы.
//
// Ключ свёртки — точное значение: содержимое кода возвращается байт-в-байт.
func aggregate(codes []Code) (unique []Code, total, collapsed int) {
	total = len(codes)
	if total == 0 {
		return nil, 0, 0
	}

	sorted := make([]Code, len(codes))
	copy(sorted, codes)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Page != sorted[j].Page {
			return sorted[i].Page < sorted[j].Page
		}
		return sorted[i].order < sorted[j].order
	})

	seen := make(map[string]struct{}, len(sorted))
	unique = make([]Code, 0, len(sorted))
	for _, code := range sorted {
		if _, exists := seen[code.Value]; exists {
			collapsed++
			continue
		}
		seen[code.Value] = struct{}{}
		unique = append(unique, code)
	}
	return unique, total, collapsed
}

// primary выбирает значение для корневого поля value: QR на самой ранней
// странице, а если QR в документе нет — первый штрихкод.
func primary(codes []Code) string {
	for _, code := range codes {
		if code.Kind == string(scan.KindQR) {
			return code.Value
		}
	}
	if len(codes) > 0 {
		return codes[0].Value
	}
	return ""
}

// bbox строит прямоугольник по точкам кода. convert переводит единицу
// координат источника в единицы ответа (пункты страницы или пиксели).
func bbox(points []image.Point, convert func(float64) float64) *BBox {
	if len(points) == 0 {
		return nil
	}

	minX, minY := points[0].X, points[0].Y
	maxX, maxY := points[0].X, points[0].Y
	for _, p := range points[1:] {
		minX = min(minX, p.X)
		minY = min(minY, p.Y)
		maxX = max(maxX, p.X)
		maxY = max(maxY, p.Y)
	}

	return &BBox{
		X:      roundTo(convert(float64(minX))),
		Y:      roundTo(convert(float64(minY))),
		Width:  roundTo(convert(float64(maxX-minX))),
		Height: roundTo(convert(float64(maxY-minY))),
	}
}

func roundTo(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}