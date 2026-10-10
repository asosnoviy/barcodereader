// Package decode связывает стадии пайплайна: извлечение встроенных картинок,
// рендер страниц и декодирование кодов, а затем агрегирует результат.
package decode

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"strings"
	"time"

	"barcodereader/internal/apierrors"
	"barcodereader/internal/config"
	"barcodereader/internal/pdfrender"
	"barcodereader/internal/pdfembedded"
	"barcodereader/internal/scan"
)

// Options — параметры разбора одного документа.
type Options struct {
	// Pages — номера страниц для сканирования (1-based). nil — все страницы.
	Pages []int
	// IncludeBarcodes включает поиск линейных штрихкодов.
	IncludeBarcodes bool
	// ReturnAllQR включает поиск всех QR на странице, а не только первого.
	ReturnAllQR bool
	// MaxPages ограничивает число сканируемых страниц.
	MaxPages int
	// DPI переопределяет стартовый DPI рендера; 0 — значение из конфигурации.
	DPI int
	// ForceRender заставляет отрендерить страницы даже если стадия A что-то
	// нашла. Нужен, когда в документе есть и встроенная картинка с QR, и
	// нарисованные векторами штрихкоды: по умолчанию ради скорости
	// рендер пропускается.
	ForceRender bool
}

// EmbeddedStage — стадия A.
type EmbeddedStage interface {
	// Extract достаёт встроенные изображения выбранных страниц (1-based);
	// пустой selection означает «первые maxPages».
	Extract(ctx context.Context, pdf []byte, selection []int, maxPages int) ([]pdfembedded.Image, error)
	// PageCount возвращает число страниц документа.
	PageCount(ctx context.Context, pdf []byte) (int, error)
}

// Renderer — стадия B.
type Renderer interface {
	Open(ctx context.Context, pdf []byte) (Document, error)
}

// Document — открытый документ в рендерере.
type Document interface {
	PageCount() int
	PageSizeInPixels(index, dpi int) (width, height int, err error)
	RenderPage(ctx context.Context, index, dpi int) (*pdfrender.Page, error)
	Close() error
}

// Service — оркестратор декодирования. Stateless между запросами.
type Service struct {
	cfg      config.Config
	renderer Renderer
	embedded EmbeddedStage
	sem      chan struct{}
	logger   *slog.Logger
}

// New собирает сервис. Семафор ограничивает число одновременно выполняющихся
// декодирований: рендер и ZXing нагружают CPU, а не только память.
func New(cfg config.Config, renderer Renderer, embedded EmbeddedStage, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	limit := cfg.MaxConcurrent
	if limit < 1 {
		limit = 1
	}
	return &Service{
		cfg:      cfg,
		renderer: renderer,
		embedded: embedded,
		sem:      make(chan struct{}, limit),
		logger:   logger,
	}
}

// Decode разбирает документ и возвращает все найденные коды.
func (s *Service) Decode(ctx context.Context, pdf []byte, opts Options) (*Result, error) {
	started := time.Now()

	opts = s.applyDefaults(opts)

	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := ctx.Err(); err != nil {
		return nil, apierrors.Timeout()
	}

	var (
		codes      []Code
		stagesUsed []string
		order      int
	)

	// Стадия A: быстрый путь через встроенные изображения.
	if s.cfg.EnableEmbeddedStage && s.embedded != nil {
		found, err := s.runEmbedded(ctx, pdf, opts)
		if err != nil {
			var apiErr *apierrors.Error
			if errors.As(err, &apiErr) {
				// Документ непригоден целиком — рендер не поможет.
				return nil, err
			}
			// Мягкая ошибка стадии: логируем и идём дальше.
			s.logger.Warn("стадия извлечения встроенных изображений не сработала", "err", err)
		} else if len(found) > 0 {
			for i := range found {
				found[i].order = order
				order++
			}
			codes = append(codes, found...)
			stagesUsed = append(stagesUsed, SourceEmbeddedImage)
		}
	}

	// Стадия B нужна, если стадия A ничего не нашла или нашла только QR,
	// а штрихкоды запрошены: линейные коды часто нарисованы векторами.
	pagesTotal, pagesScanned := 0, 0
	if s.shouldRender(codes, opts) {
		rendered, err := s.runRender(ctx, pdf, opts)
		if err != nil {
			return nil, err
		}
		pagesTotal = rendered.pagesTotal
		pagesScanned = rendered.pagesScanned
		if len(rendered.codes) > 0 {
			stagesUsed = append(stagesUsed, SourceRenderedPage)
			opts.DPI = rendered.dpi
		}
		for i := range rendered.codes {
			rendered.codes[i].order = order
			order++
		}
		codes = append(codes, rendered.codes...)
	}
	if pagesTotal == 0 {
		pagesTotal = s.documentPages(ctx, pdf, opts)
	}

	unique, total, collapsed := aggregate(codes)

	result := &Result{
		Found: len(unique) > 0,
		Codes: unique,
		Meta: Meta{
			PagesTotal:          pagesTotal,
			PagesScanned:        pagesScanned,
			DPI:                 opts.DPI,
			StagesUsed:          stagesUsed,
			CodesTotal:          total,
			CodesUnique:         len(unique),
			DuplicatesCollapsed: collapsed,
			ElapsedMS:           time.Since(started).Milliseconds(),
		},
	}
	if result.Meta.StagesUsed == nil {
		result.Meta.StagesUsed = []string{}
	}
	result.Value = primary(unique)

	if !result.Found {
		return nil, apierrors.NoCodesFound(map[string]any{
			"pages_scanned": result.Meta.PagesScanned,
			"pages_total":   result.Meta.PagesTotal,
		})
	}
	return result, nil
}

func (s *Service) applyDefaults(opts Options) Options {
	// Булевы опции не дефолтизируются здесь намеренно: явный false от
	// клиента должен означать «не искать», а не «взять значение из конфига».
	// Конфигурацию в дефолты подставляет вызывающий код (HTTP-слой).
	if opts.MaxPages <= 0 {
		opts.MaxPages = s.cfg.MaxPages
	}
	if opts.DPI <= 0 {
		opts.DPI = s.cfg.RenderDPI
	}
	if len(opts.Pages) > opts.MaxPages {
		opts.Pages = opts.Pages[:opts.MaxPages]
	}
	return opts
}

// documentPages узнаёт число страниц без рендера: рендер запускать только
// ради счётчика слишком дорого.
func (s *Service) documentPages(ctx context.Context, pdf []byte, opts Options) int {
	if len(opts.Pages) > 0 {
		return len(opts.Pages)
	}
	if s.embedded != nil {
		if total, err := s.embedded.PageCount(ctx, pdf); err == nil && total > 0 {
			return total
		}
	}
	return opts.MaxPages
}

func (s *Service) acquire(ctx context.Context) (func(), error) {
	timer := time.NewTimer(s.cfg.QueueWait)
	defer timer.Stop()

	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	case <-timer.C:
		return nil, apierrors.TooManyRequests(s.cfg.QueueWait.String())
	case <-ctx.Done():
		return nil, apierrors.Timeout()
	}
}

// shouldRender решает, нужен ли рендер. Рендер дороже извлечения картинок,
// но это единственный способ увидеть векторные коды и сканы целой страницы.
// По умолчанию он запускается только тогда, когда стадия A ничего не нашла;
// если нужен полный обход документа, клиент просит это явно через ForceRender.
func (s *Service) shouldRender(codes []Code, opts Options) bool {
	if s.renderer == nil {
		return false
	}
	return opts.ForceRender || len(codes) == 0
}

// runEmbedded декодирует коды во встроенных изображениях.
func (s *Service) runEmbedded(ctx context.Context, pdf []byte, opts Options) ([]Code, error) {
	images, err := s.embedded.Extract(ctx, pdf, opts.Pages, opts.MaxPages)
	if err != nil {
		return nil, err
	}

	scanOpts := s.scanOptions(opts)
	var codes []Code
	for _, embedded := range images {
		if err := ctx.Err(); err != nil {
			return nil, apierrors.Timeout()
		}
		for _, found := range scan.Scan(ctx, embedded.Img, scanOpts) {
			codes = append(codes, Code{
				Value:       found.Value,
				Kind:        string(found.Kind),
				Format:      found.Format,
				Page:        embedded.PageNr,
				Source:      SourceEmbeddedImage,
				ImageObject: embedded.ObjNr,
				BBox:        bbox(found.Points, func(v float64) float64 { return v }),
				Points:      found.Points,
			})
		}
	}
	return codes, nil
}

// renderOutcome — результат стадии B.
type renderOutcome struct {
	codes         []Code
	dpi           int
	pagesTotal    int
	pagesScanned  int
}

// runRender декодирует коды на отрендеренных страницах с лестницей DPI.
func (s *Service) runRender(ctx context.Context, pdf []byte, opts Options) (renderOutcome, error) {
	out := renderOutcome{dpi: opts.DPI}

	doc, err := s.renderer.Open(ctx, pdf)
	if err != nil {
		// Документ не открылся: почти всегда это битый или защищённый файл,
		// а не сбой сервиса.
		return out, classifyOpenError(err)
	}
	defer doc.Close()

	out.pagesTotal = doc.PageCount()
	pages := s.pagesToScan(out.pagesTotal, opts)
	if len(pages) == 0 {
		return out, nil
	}

	scanOpts := s.scanOptions(opts)

	for pass, dpiValue := range s.dpiLadder(opts) {
		for _, page := range pages {
			if err := ctx.Err(); err != nil {
				return out, apierrors.Timeout()
			}
			// Повторный проход имеет смысл только пока ничего не найдено.
			if pass > 0 && len(out.codes) > 0 {
				break
			}

			pageDPI := s.fitDPI(doc, page, dpiValue)
			rendered, err := doc.RenderPage(ctx, page, pageDPI)
			if err != nil {
				return out, classifyRenderError(err)
			}

			convert := rendered.PixelsToPoints
			for _, found := range scan.Scan(ctx, rendered.Image, scanOpts) {
				points := convertPoints(found.Points, convert)
				out.codes = append(out.codes, Code{
					Value:  found.Value,
					Kind:   string(found.Kind),
					Format: found.Format,
					Page:   page + 1,
					Source: SourceRenderedPage,
					BBox:   bbox(points, func(v float64) float64 { return v }),
					Points: found.Points,
				})
			}
			out.dpi = pageDPI
			out.pagesScanned++
		}
		if len(out.codes) > 0 {
			break
		}
	}
	return out, nil
}

// dpiLadder возвращает список DPI для попыток: стартовый и, если разрешено
// повторное декодирование, повышенный.
func (s *Service) dpiLadder(opts Options) []int {
	if s.cfg.RetryHigherDPI && s.cfg.RenderDPIMax > opts.DPI {
		return []int{opts.DPI, s.cfg.RenderDPIMax}
	}
	return []int{opts.DPI}
}

func (s *Service) fitDPI(doc Document, page, dpi int) int {
	width, height, err := doc.PageSizeInPixels(page, dpi)
	if err != nil {
		return dpi
	}
	return pdfrender.FitDPI(width, height, dpi, s.cfg.MaxRenderPixels)
}

// pagesToScan переводит выборку страниц в индексы (0-based) с учётом лимитов.
func (s *Service) pagesToScan(total int, opts Options) []int {
	if total <= 0 {
		return nil
	}
	if len(opts.Pages) == 0 {
		limit := min(opts.MaxPages, total)
		pages := make([]int, 0, limit)
		for i := range limit {
			pages = append(pages, i)
		}
		return pages
	}

	pages := make([]int, 0, len(opts.Pages))
	for _, number := range opts.Pages {
		index := number - 1
		if index < 0 || index >= total {
			continue
		}
		pages = append(pages, index)
	}
	return pages
}

func (s *Service) scanOptions(opts Options) scan.Options {
	return scan.Options{
		MaxImageDim:     s.cfg.MaxImageDim,
		OneDMaxWidth:    s.cfg.OneDMaxWidth,
		IncludeBarcodes: opts.IncludeBarcodes,
		ReturnAllQR:     opts.ReturnAllQR,
	}
}

// convertPoints переводит координаты ZXing в единицы ответа.
func convertPoints(points []image.Point, convert func(float64) float64) []image.Point {
	if len(points) == 0 || convert == nil {
		return points
	}
	out := make([]image.Point, 0, len(points))
	for _, p := range points {
		out = append(out, image.Pt(
			int(roundTo(convert(float64(p.X)))),
			int(roundTo(convert(float64(p.Y)))),
		))
	}
	return out
}

// classifyOpenError переводит ошибку открытия документа: PDFium не смог
// разобрать файл, значит для клиента это 422, а не 500.
func classifyOpenError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return apierrors.Timeout()
	}
	if isCapacityError(err) {
		return apierrors.NoCapacity(err)
	}
	if isEncryptedError(err) {
		return apierrors.PDFEncrypted(err)
	}
	return apierrors.PDFUnreadable(err)
}

// classifyRenderError переводит ошибки рендера страницы.
func classifyRenderError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return apierrors.Timeout()
	}
	if isCapacityError(err) {
		return apierrors.NoCapacity(err)
	}
	if isEncryptedError(err) {
		return apierrors.PDFEncrypted(err)
	}
	return apierrors.Internal(err)
}

func isEncryptedError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "password") || strings.Contains(msg, "encrypted")
}

func isCapacityError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no free instance") ||
		strings.Contains(msg, "instance") && strings.Contains(msg, "pool")
}