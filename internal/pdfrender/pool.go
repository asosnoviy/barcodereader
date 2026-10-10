// Package pdfrender растеризирует страницы PDF через PDFium, скомпилированный в
// WebAssembly (runtime Wazero). Такой бэкенд не требует cgo и собирается в
// один бинарник под Windows/Linux/Docker.
package pdfrender

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"sync"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// Renderer владеет пулом инстансов PDFium. Создаётся один раз на старте
// сервиса и закрывается при shutdown.
type Renderer struct {
	pool pdfium.Pool
}

// NewRenderer инициализирует wasm-пул. workers — число одновременно живых
// инстансов: каждый из них компилирует модуль Wazero, поэтому лишние
// инстансы одновременно только замедляют старт.
func NewRenderer(workers int) (*Renderer, error) {
	if workers < 1 {
		workers = 1
	}
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle: workers,
		MaxIdle: workers,
		MaxTotal: workers * 2,
	})
	if err != nil {
		return nil, fmt.Errorf("инициализация пула PDFium: %w", err)
	}
	return &Renderer{pool: pool}, nil
}

// Close освобождает пул. Вызывается только при shutdown: после него
// открытие документов невозможно.
func (r *Renderer) Close() {
	if r == nil || r.pool == nil {
		return
	}
	r.pool.Close()
}

// Document — открытый PDF вместе с инстансом PDFium, который его обслуживает.
// Инстанс возвращается в пул только в Close, поэтому Document нельзя
// копировать и нельзя использовать после Close.
type Document struct {
	inst    pdfium.Pdfium
	doc     *responses.OpenDocument
	pages   int
	closeMu sync.Mutex
	closed  bool
}

// Open берёт инстанс из пула и открывает документ. При ошибке инстанс
// сразу возвращается в пул.
func (r *Renderer) Open(ctx context.Context, pdf []byte) (*Document, error) {
	inst, err := r.pool.GetInstanceWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить инстанс PDFium: %w", err)
	}

	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		inst.Close()
		return nil, fmt.Errorf("открытие документа: %w", err)
	}

	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		_, _ = inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		inst.Close()
		return nil, fmt.Errorf("подсчёт страниц: %w", err)
	}

	slog.Debug("документ открыт", "pages", count.PageCount)
	return &Document{inst: inst, doc: doc, pages: count.PageCount}, nil
}

// PageCount возвращает число страниц документа.
func (d *Document) PageCount() int { return d.pages }

// Close закрывает документ и возвращает инстанс в пул. Повторный вызов безопасен.
func (d *Document) Close() error {
	d.closeMu.Lock()
	defer d.closeMu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true

	_, err := d.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.doc.Document})
	d.inst.Close() // освобождение инстанса происходит в любом случае
	if err != nil {
		return fmt.Errorf("закрытие документа: %w", err)
	}
	return nil
}

// Page содержит результат растеризации одной страницы.
type Page struct {
	// Image — серое изображение страницы (*image.Gray).
	Image image.Image
	// DPI фактически использованный при рендере.
	DPI int
	// PointsPerPixel — коэффициент перевода пикселей в пункты страницы.
	PointsPerPixel float64
}

// PixelsToPoints переводит координату пикселя в пункты страницы.
func (p Page) PixelsToPoints(px float64) float64 {
	if p.PointsPerPixel <= 0 {
		return px
	}
	return px * p.PointsPerPixel
}

// RenderPage растеризирует страницу (индекс 0-based) в оттенках серого.
func (d *Document) RenderPage(ctx context.Context, index, dpi int) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	page := requests.Page{ByIndex: &requests.PageByIndex{
		Document: d.doc.Document,
		Index:    index,
	}}

	rendered, err := d.inst.RenderPageInDPI(&requests.RenderPageInDPI{
		Page:        page,
		DPI:         dpi,
		ImageFormat: requests.RenderImageFormatGrayscale,
	})
	if err != nil {
		return nil, fmt.Errorf("рендер страницы %d: %w", index+1, err)
	}
	// В wasm буфер живёт только до Cleanup: возвращать наружу нужно
	// изображение, снятое с этого буфера.
	defer rendered.Cleanup()

	img := rendered.Result.RenderedImage
	if img == nil {
		return nil, fmt.Errorf("рендер страницы %d вернул пустое изображение", index+1)
	}

	return &Page{
		Image:          img,
		DPI:            dpi,
		PointsPerPixel: rendered.Result.PointToPixelRatio,
	}, nil
}

// PageSizeInPixels возвращает размер страницы в пикселях при заданном DPI.
// Нужен, чтобы не отрендерить страницу дороже лимита по памяти.
func (d *Document) PageSizeInPixels(index, dpi int) (width, height int, err error) {
	size, err := d.inst.GetPageSizeInPixels(&requests.GetPageSizeInPixels{
		Page: requests.Page{ByIndex: &requests.PageByIndex{
			Document: d.doc.Document,
			Index:    index,
		}},
		DPI: dpi,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("размер страницы %d: %w", index+1, err)
	}
	return size.Width, size.Height, nil
}