package decode

import (
	"context"
	"errors"
	"image"
	"testing"

	"barcodereader/internal/apierrors"
	"barcodereader/internal/pdfembedded"
	"barcodereader/internal/testimages"
)

// Регрессия: под службой Windows отчёт pdfcpu об ошибке формата
// отбрасывался досрочно, и сервис отвечал 422, не дав шанса PDFium.
//
// Наблюдалось на реальном файле ozno_sc.pdf: в Docker и в консоли он
// разбирался, а под службой возвращал pdf_unreadable. Причина в том, что
// стадия A возвращала ошибку сразу, без попытки отрендерить документ.
func TestEmbeddedStageErrorDoesNotBlockRender(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = true
	cfg.RetryHigherDPI = false

	// Стадия A отвергает документ как непригодный...
	embedded := &stubEmbedded{err: apierrors.PDFUnreadable(errors.New("pdfcpu: invalid"))}
	// ...а стадия B с ним справляется.
	doc := &stubDocument{pages: 1, images: map[int]image.Image{
		0: testimages.QR("НАЙДЕНО", 300),
	}}
	svc := New(cfg, &stubRenderer{doc: doc}, embedded, quietLogger())

	res, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err != nil {
		t.Fatalf("рендер мог бы разобрать документ, но получена ошибка: %v", err)
	}
	if !res.Found {
		t.Fatal("код не найден")
	}
	if embedded.calls == 0 {
		t.Error("стадия A не вызывалась")
	}
	if res.Meta.StagesUsed == nil || len(res.Meta.StagesUsed) == 0 {
		t.Error("стадии не отражены в ответе")
	}
}

// Если обе стадии не справились, ошибка формата всё-таки возвращается:
// смягчать отказ нельзя, документ действительно нечитаемый.
func TestBothStagesFailingStillReportsUnreadable(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = true
	cfg.RetryHigherDPI = false

	embedded := &stubEmbedded{err: apierrors.PDFUnreadable(errors.New("pdfcpu: malformed"))}
	// Стадия B тоже падает — документ не открывается рендерером.
	empty := &stubDocument{pages: 1}
	renderer := &stubRenderer{doc: empty, err: errors.New("open failed")}
	svc := New(cfg, renderer, embedded, quietLogger())

	_, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err == nil {
		t.Fatal("ожидалась ошибка: обе стадии не справились")
	}

	var apiErr *apierrors.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ожидалась apierrors.Error, получено %T", err)
	}
	if apiErr.Code != "pdf_unreadable" {
		t.Errorf("code = %q, ожидался pdf_unreadable", apiErr.Code)
	}
}

// Отказ окружения (таймаут, нехватка ёмкости) не должен маскироваться
// под «документ повреждён»: клиент должен видеть настоящую причину,
// даже если стадия A отвергла документ.
func TestRenderInfrastructureErrorWinsOverEmbedded(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = true
	cfg.RetryHigherDPI = false

	// Стадия A отвергает документ по формату...
	embedded := &stubEmbedded{err: apierrors.PDFUnreadable(errors.New("pdfcpu: invalid"))}
	// ...а рендер упирается в таймаут.
	doc := &stubDocument{pages: 1}
	renderer := &stubRenderer{doc: doc, err: context.DeadlineExceeded}
	svc := New(cfg, renderer, embedded, quietLogger())

	_, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	var apiErr *apierrors.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ожидалась apierrors.Error, получено %T", err)
	}
	if apiErr.Code != "timeout" {
		t.Errorf("code = %q, ожидался timeout: отказ окружения нельзя выдавать за pdf_unreadable", apiErr.Code)
	}
}

// Мягкая ошибка стадии A не должна выходить наружу: клиент о ней не знает,
// и без перехвата она превратилась бы в 500 «внутренняя ошибка».
//
// Именно так вела себя настоящая фикстура corrupt.pdf: pdfcpu отвечал
// «missing root dict», что не распознавалось как «повреждён», ошибка
// оставалась мягкой, а отдать её клиенту было нельзя — вердикт о документе
// в этом случае принадлежит рендеру.
func TestSoftEmbeddedErrorNeverLeaksToClient(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = true
	cfg.RetryHigherDPI = false

	embedded := &stubEmbedded{err: pdfembedded.ErrStageFailed}
	renderer := &stubRenderer{
		doc: &stubDocument{pages: 1},
		err: apierrors.PDFUnreadable(errors.New("incorrect format")),
	}
	svc := New(cfg, renderer, embedded, quietLogger())

	_, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err == nil {
		t.Fatal("ожидалась ошибка: обе стадии не справились")
	}

	var apiErr *apierrors.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("наружу ушла не apierrors.Error, а %T: её не понимает HTTP-слой", err)
	}
	if apiErr.Code == "internal_error" {
		t.Error("мягкая ошибка стадии A дошла до клиента как internal_error")
	}
	if apiErr.Code != "pdf_unreadable" {
		t.Errorf("code = %q, ожидался pdf_unreadable от рендера", apiErr.Code)
	}
}

func TestIsInfrastructuralError(t *testing.T) {
	if !isInfrastructuralError(apierrors.Timeout()) {
		t.Error("таймаут должен считаться инфраструктурной ошибкой")
	}
	if !isInfrastructuralError(apierrors.NoCapacity(errors.New("x"))) {
		t.Error("нехватка ёмкости должна считаться инфраструктурной ошибкой")
	}
	if isInfrastructuralError(apierrors.PDFUnreadable(errors.New("x"))) {
		t.Error("отказ по формату не инфраструктурный")
	}
	if isInfrastructuralError(errors.New("обычная ошибка")) {
		t.Error("не-apierrors не должна считаться инфраструктурной")
	}
}

// Ошибка стадии A, не связанная с форматом (мягкая), и раньше не должна
// была мешать рендеру — поведение не изменилось.
func TestSoftEmbeddedErrorStillFallsThroughToRender(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = true
	cfg.RetryHigherDPI = false

	embedded := &stubEmbedded{err: pdfembedded.ErrStageFailed}
	doc := &stubDocument{pages: 1, images: map[int]image.Image{
		0: testimages.QR("НАЙДЕНО", 300),
	}}
	svc := New(cfg, &stubRenderer{doc: doc}, embedded, quietLogger())

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions()); err != nil {
		t.Fatalf("мягкая ошибка стадии A не должна ломать запрос: %v", err)
	}
}