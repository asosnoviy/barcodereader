// Package httpapi реализует HTTP-контракт сервиса: приём PDF в base64,
// вызов декодирования и формирование JSON-ответа.
package httpapi

import (
	"context"

	"barcodereader/internal/decode"
)

// Decoder — декодировщик документов (реализуется *decode.Service).
type Decoder interface {
	Decode(ctx context.Context, pdf []byte, opts decode.Options) (*decode.Result, error)
}

// decodeRequest — тело запроса в JSON.
type decodeRequest struct {
	PDFBase64 string         `json:"pdf_base64"`
	Options   *requestOption `json:"options,omitempty"`
}

// requestOption — необязательные параметры разбора. Неизвестные поля
// игнорируются: так добавление новых опций не ломает старых клиентов.
type requestOption struct {
	Pages           string `json:"pages,omitempty"`
	IncludeBarcodes *bool  `json:"include_barcodes,omitempty"`
	ReturnAllQR     *bool  `json:"return_all_qr,omitempty"`
	MaxPages        *int   `json:"max_pages,omitempty"`
	DPI             *int   `json:"dpi,omitempty"`
	ForceRender     bool   `json:"force_render,omitempty"`
}

// bboxDTO — прямоугольник кода.
type bboxDTO struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// codeDTO — запись в codes[].
type codeDTO struct {
	Value       string    `json:"value"`
	Kind        string    `json:"kind"`
	Format      string    `json:"format"`
	Page        int       `json:"page"`
	Source      string    `json:"source"`
	ImageObject *int      `json:"image_object,omitempty"`
	BBox        *bboxDTO  `json:"bbox_points,omitempty"`
	Points      *[]point  `json:"points,omitempty"`
}

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// metaDTO — служебная информация о разборе.
type metaDTO struct {
	PagesTotal          int      `json:"pages_total"`
	PagesScanned        int      `json:"pages_scanned"`
	DPI                 int      `json:"dpi"`
	StagesUsed          []string `json:"stages_used"`
	CodesTotal          int      `json:"codes_total"`
	CodesUnique         int      `json:"codes_unique"`
	DuplicatesCollapsed int      `json:"duplicates_collapsed"`
	ElapsedMS           int64    `json:"elapsed_ms"`
}

// decodeResponse — тело успешного ответа.
type decodeResponse struct {
	RequestID string   `json:"request_id"`
	Found     bool     `json:"found"`
	Value     string   `json:"value,omitempty"`
	Codes     []codeDTO `json:"codes"`
	Meta      *metaDTO `json:"meta,omitempty"`
}

// errorDTO — тело ошибочного ответа.
type errorDTO struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// errorResponse — ошибочный ответ в обёртке сервиса.
type errorResponse struct {
	RequestID string    `json:"request_id"`
	Found     bool      `json:"found"`
	Error     errorDTO  `json:"error"`
}

// toResponse переводит результат декодирования в DTO ответа.
func toResponse(requestID string, result *decode.Result) decodeResponse {
	codes := make([]codeDTO, 0, len(result.Codes))
	for _, code := range result.Codes {
		codes = append(codes, toCodeDTO(code))
	}

	return decodeResponse{
		RequestID: requestID,
		Found:     result.Found,
		Value:     result.Value,
		Codes:     codes,
		Meta: &metaDTO{
			PagesTotal:          result.Meta.PagesTotal,
			PagesScanned:        result.Meta.PagesScanned,
			DPI:                 result.Meta.DPI,
			StagesUsed:          result.Meta.StagesUsed,
			CodesTotal:          result.Meta.CodesTotal,
			CodesUnique:         result.Meta.CodesUnique,
			DuplicatesCollapsed: result.Meta.DuplicatesCollapsed,
			ElapsedMS:           result.Meta.ElapsedMS,
		},
	}
}

func toCodeDTO(code decode.Code) codeDTO {
	dto := codeDTO{
		Value:  code.Value,
		Kind:   code.Kind,
		Format: code.Format,
		Page:   code.Page,
		Source: code.Source,
	}

	// image_object имеет смысл только для встроенных изображений.
	if code.Source == decode.SourceEmbeddedImage && code.ImageObject > 0 {
		obj := code.ImageObject
		dto.ImageObject = &obj
	}

	if code.BBox != nil {
		dto.BBox = &bboxDTO{
			X:      code.BBox.X,
			Y:      code.BBox.Y,
			Width:  code.BBox.Width,
			Height: code.BBox.Height,
		}
	}

	// Точки приходят в единицах источника: для встроенной картинки это
	// пиксели, для отрендеренной страницы — пункты. Чтобы не путать
	// единицы, координаты отдаём только там, где они однозначны.
	if code.Source == decode.SourceEmbeddedImage && len(code.Points) > 0 {
		pts := make([]point, 0, len(code.Points))
		for _, p := range code.Points {
			pts = append(pts, point{X: float64(p.X), Y: float64(p.Y)})
		}
		dto.Points = &pts
	}

	return dto
}