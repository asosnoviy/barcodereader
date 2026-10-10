package scan

import (
	"context"
	"image"
	"sort"
)

// Поиск мелких двумерных символов по тёмным областям.
//
// Детектор Data Matrix в ZXing (WhiteRectangleDetector) начинает осмотр
// **от центра кадра** и расширяет прямоугольник наружу. Поэтому символ,
// стоящий не по центру — а на этикетке он почти всегда сдвинут, — не
// находится: поиск упирается в границу страницы и возвращает NotFound.
// Увеличение кода или изменение порога не помогает.
//
// Обходной путь — найти тёмные связные области (кандидаты), обрезать каждую
// с полями и декодировать уже этот фрагмент: символ оказывается в центре
// кадра, и штатный детектор работает.

// Параметры поиска областей.
const (
	// regionSearchDim — сторона рабочей копии, на которой ищутся связные
	// области. Кандидаты вычисляются на уменьшенной копии (быстро), а
	// декодируется уже оригинал — качество модулей не теряется.
	regionSearchDim = 1400
	// regionMinSide — минимальная сторона кандидата в пикселях оригинала.
	// Меньше — слишком мелкий шрифт, больше — скорее всего не символ.
	regionMinSide = 24
	// regionMaxRegions ограничивает число декодируемых кандидатов: на
	// странице с текстом связных областей сотни, а дорогие попытки
	// декодирования ограничены бюджетом времени.
	regionMaxRegions = 40
	// regionMarginMin — минимальное поле вокруг кандидата.
	regionMarginMin = 6
	// regionAspectMin/regionAspectMax допускают перекос: символ может быть
	// повёрнут или смазан, и его bbox перестаёт быть квадратом.
	regionAspectMin = 0.35
	regionAspectMax = 2.8
)

// ScanRegions ищет двумерные символы по тёмным областям страницы.
//
// Вызывается как последний рубеж: обычный поиск уже ничего не нашёл.
func ScanRegions(ctx context.Context, img image.Image, opts Options, margin int) []Code {
	if img == nil {
		return nil
	}
	rects := findDarkRegions(img)
	if len(rects) == 0 {
		return nil
	}

	var found []Code
	for _, rect := range rects {
		if err := ctx.Err(); err != nil {
			return found
		}

		x0, y0, x1, y1 := expandRect(rect, margin, img.Bounds())
		sub := cropRGBA(img, x0, y0, x1, y1)
		if sub == nil {
			continue
		}
		for _, code := range scanDataMatrix(ctx, sub, opts) {
			found = append(found, shiftCode(code, x0, y0))
		}
	}
	return found
}

// findDarkRegions возвращает прямоугольники-кандидаты в координатах оригинала.
func findDarkRegions(img image.Image) []image.Rectangle {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < regionMinSide || height < regionMinSide {
		return nil
	}

	// Рабочая копия: уменьшаем, чтобы связные области искать дёшево.
	scale := 1.0
	work := img
	if maxInt(width, height) > regionSearchDim {
		scale = float64(regionSearchDim) / float64(maxInt(width, height))
		nw := maxInt(1, int(float64(width)*scale+0.5))
		nh := maxInt(1, int(float64(height)*scale+0.5))
		work = Downscale(img, maxInt(nw, nh))
	}
	gray := ToGray(work)
	if gray == nil {
		return nil
	}
	workW, workH := gray.Bounds().Dx(), gray.Bounds().Dy()
	if workW < 2 || workH < 2 {
		return nil
	}

	dark := otsuThreshold(gray)
	mask := darkMask(gray, dark)

	// Обход в обратном масштабе: координаты кандидата сразу переводим
	// в систему исходного изображения.
	inv := 1 / scale
	minSide := int(float64(regionMinSide)*scale)
	maxSide := maxInt(workW, workH)

	boxes := connectedBoxes(mask, workW, workH, minSide, maxSide)
	rects := make([]image.Rectangle, 0, len(boxes))
	for _, box := range boxes {
		x0 := bounds.Min.X + int(float64(box.Min.X)*inv)
		y0 := bounds.Min.Y + int(float64(box.Min.Y)*inv)
		x1 := bounds.Min.X + int(float64(box.Max.X)*inv)
		y1 := bounds.Min.Y + int(float64(box.Max.Y)*inv)

		w, h := x1-x0, y1-y0
		if w < regionMinSide || h < regionMinSide {
			continue
		}
		aspect := float64(w) / float64(h)
		if aspect < regionAspectMin || aspect > regionAspectMax {
			continue
		}
		rects = append(rects, image.Rect(x0, y0, x1, y1))
	}

	// Крупные кандидаты первыми: символ крупнее — вероятнее он и есть.
	sort.SliceStable(rects, func(i, j int) bool {
		ai := area(rects[i])
		aj := area(rects[j])
		return ai > aj
	})
	if len(rects) > regionMaxRegions {
		rects = rects[:regionMaxRegions]
	}
	return rects
}

// expandRect добавляет к прямоугольнику поле тишины и обрезает по кадру.
func expandRect(rect image.Rectangle, margin int, bounds image.Rectangle) (x0, y0, x1, y1 int) {
	if margin <= 0 {
		margin = regionMarginMin
	}
	// Поле по размеру кандидата: тишина у 2D-кодов измеряется в модулях,
	// поэтому у мелкого символа и поля должны быть мелкими.
	side := maxInt(rect.Dx(), rect.Dy())
	if grow := side / 4; grow > margin {
		margin = grow
	}

	x0 = maxInt(bounds.Min.X, rect.Min.X-margin)
	y0 = maxInt(bounds.Min.Y, rect.Min.Y-margin)
	x1 = minInt(bounds.Max.X, rect.Max.X+margin)
	y1 = minInt(bounds.Max.Y, rect.Max.Y+margin)
	return x0, y0, x1, y1
}

// otsuThreshold выбирает порог бинаризации методом Оцу по гистограмме
// яркости: он устойчив к засветке и не требует подбора константы.
func otsuThreshold(gray *image.Gray) int {
	var hist [256]int
	b := gray.Bounds()
	for _, p := range gray.Pix {
		hist[p]++
	}
	total := len(gray.Pix)
	if total == 0 {
		return 128
	}

	sum := 0
	for i, c := range hist {
		sum += i * c
	}
	sumB, wB := 0, 0
	best, bestVar := 128, -1.0
	for i := 0; i < 256; i++ {
		wB += hist[i]
		if wB == 0 {
			continue
		}
		wF := total - wB
		if wF == 0 {
			break
		}
		sumB += i * hist[i]
		mB := float64(sumB) / float64(wB)
		mF := float64(sum-sumB) / float64(wF)
		variance := float64(wB) * float64(wF) * (mB - mF) * (mB - mF)
		if variance > bestVar {
			bestVar, best = variance, i
		}
	}
	_ = b
	return best
}

// darkMask отмечает тёмные пиксели рабочей копии.
func darkMask(gray *image.Gray, threshold int) []bool {
	mask := make([]bool, len(gray.Pix))
	// Небольшой допуск вниз: у символов тонкие модули, и жёсткий порог
	// может «съесть» целую полосу, изменив число колонок.
	for i, p := range gray.Pix {
		mask[i] = int(p) <= threshold+8
	}
	return mask
}

// connectedBoxes находит связные области тёмных пикселей и возвращает их
// ограничивающие прямоугольники.
//
// Обход — итеративный DFS по стеку: рекурсия на картинке в несколько
// мегапикселей переполнила бы стек.
func connectedBoxes(mask []bool, width, height, minSide, maxSide int) []image.Rectangle {
	visited := make([]bool, len(mask))
	var boxes []image.Rectangle
	stack := make([]int, 0, 1024)

	for y := range height {
		for x := range width {
			start := y*width + x
			if !mask[start] || visited[start] {
				continue
			}

			minX, maxX, minY, maxY := x, x, y, y
			stack = append(stack[:0], start)
			visited[start] = true

			for len(stack) > 0 {
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				px, py := p%width, p/width
				if px < minX {
					minX = px
				}
				if px > maxX {
					maxX = px
				}
				if py < minY {
					minY = py
				}
				if py > maxY {
					maxY = py
				}

				// Восемь соседей: диагонали связывают модули, идущие
				// «ступенькой», — без них один символ распадался бы на части.
				for dy := -1; dy <= 1; dy++ {
					ny := py + dy
					if ny < 0 || ny >= height {
						continue
					}
					for dx := -1; dx <= 1; dx++ {
						nx := px + dx
						if nx < 0 || nx >= width {
							continue
						}
						q := ny*width + nx
						if !mask[q] || visited[q] {
							continue
						}
						visited[q] = true
						stack = append(stack, q)
					}
				}
			}

			w := maxX - minX + 1
			h := maxY - minY + 1
			if w < minSide || h < minSide || w > maxSide || h > maxSide {
				continue
			}
			boxes = append(boxes, image.Rect(minX, minY, maxX+1, maxY+1))
		}
	}
	return boxes
}

// cropRGBA вырезает прямоугольник в координатах кадра в отдельное изображение.
func cropRGBA(img image.Image, x0, y0, x1, y1 int) image.Image {
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	b := img.Bounds()
	x0 = maxInt(b.Min.X, x0)
	y0 = maxInt(b.Min.Y, y0)
	x1 = minInt(b.Max.X, x1)
	y1 = minInt(b.Max.Y, y1)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}

	gray := ToGray(img)
	if gray == nil {
		return nil
	}
	dst := image.NewGray(image.Rect(0, 0, x1-x0, y1-y0))
	for y := y0; y < y1; y++ {
		off := gray.PixOffset(x0-b.Min.X, y-b.Min.Y)
		copy(dst.Pix[dst.PixOffset(0, y-y0):], gray.Pix[off:off+(x1-x0)])
	}
	return dst
}

func area(r image.Rectangle) int { return r.Dx() * r.Dy() }

// shiftCode переносит координаты кода из координат среза в координаты
// исходной страницы.
func shiftCode(code Code, dx, dy int) Code {
	if len(code.Points) > 0 {
		points := make([]image.Point, 0, len(code.Points))
		for _, p := range code.Points {
			points = append(points, image.Pt(p.X+dx, p.Y+dy))
		}
		code.Points = points
	}
	return code
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}