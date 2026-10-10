package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"barcodereader/internal/apierrors"
	"barcodereader/internal/decode"
)

// pdfMagic — сигнатура PDF-файла.
var pdfMagic = []byte("%PDF-")

// extractPDF достаёт байты PDF из тела запроса независимо от формата:
// либо из JSON-поля pdf_base64, либо напрямую (application/pdf).
func extractPDF(contentType string, jsonBody *decodeRequest, raw []byte, maxPDFBytes int64) ([]byte, error) {
	pdf, err := decodeBase64Field(jsonBody, raw, contentType)
	if err != nil {
		return nil, err
	}

	if int64(len(pdf)) > maxPDFBytes {
		return nil, apierrors.PayloadTooLarge(maxPDFBytes)
	}
	if len(pdf) < len(pdfMagic) || string(pdf[:len(pdfMagic)]) != string(pdfMagic) {
		return nil, apierrors.NotAPDF()
	}
	return pdf, nil
}

func decodeBase64Field(jsonBody *decodeRequest, raw []byte, contentType string) ([]byte, error) {
	if jsonBody == nil {
		return raw, nil
	}

	encoded := strings.TrimSpace(jsonBody.PDFBase64)
	if encoded == "" {
		return nil, apierrors.MissingField("pdf_base64")
	}
	return decodeBase64(encoded)
}

// decodeBase64 принимает стандартный и URL-безопасный алфавит, с переводами
// строк внутри значения и без padding — так приходят файлы из разных клиентов.
func decodeBase64(encoded string) ([]byte, error) {
	cleaned := stripWhitespace(encoded)

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}

	var lastErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(cleaned)
		if err == nil {
			return decoded, nil
		}
		// Заполняем пропущенный padding: опущенные символы «=» —
		// самая частая причина отказа при ручной генерации base64.
		if padded := padBase64(cleaned, encoding); padded != cleaned {
			if decoded, err := encoding.DecodeString(padded); err == nil {
				return decoded, nil
			}
		}
		lastErr = err
	}
	return nil, apierrors.InvalidBase64(lastErr)
}

func stripWhitespace(s string) string {
	if !strings.ContainsAny(s, " \t\r\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func padBase64(s string, encoding *base64.Encoding) string {
	pad := len(s) % 4
	if pad == 0 {
		return s
	}
	return s + strings.Repeat("=", 4-pad)
}

// buildOptions переводит пользовательские опции в параметры декодирования.
// Дефолты берутся из конфигурации здесь, а не в decode: явный false от клиента
// («не искать штрихкоды») обязан переживать переход по слоям.
func buildOptions(opts *requestOption, defaults decode.Options) (decode.Options, error) {
	if opts == nil {
		return defaults, nil
	}

	out := defaults
	if opts.IncludeBarcodes != nil {
		out.IncludeBarcodes = *opts.IncludeBarcodes
	}
	if opts.ReturnAllQR != nil {
		out.ReturnAllQR = *opts.ReturnAllQR
	}
	if opts.MaxPages != nil {
		if *opts.MaxPages <= 0 {
			return out, apierrors.InvalidPages(strconv.Itoa(*opts.MaxPages), errors.New("max_pages должен быть > 0"))
		}
		out.MaxPages = *opts.MaxPages
	}
	if opts.DPI != nil {
		if *opts.DPI <= 0 {
			return out, apierrors.InvalidPages(strconv.Itoa(*opts.DPI), errors.New("dpi должен быть > 0"))
		}
		out.DPI = *opts.DPI
	}
	out.ForceRender = opts.ForceRender

	if pages := strings.TrimSpace(opts.Pages); pages != "" {
		parsed, err := parsePageSelection(pages)
		if err != nil {
			return out, err
		}
		out.Pages = parsed
	}
	return out, nil
}

// parsePageSelection разбирает "1", "1-3", "1,3,5-7" в список номеров страниц.
// Дубликаты и пересечения диапазонов схлопываются.
func parsePageSelection(selection string) ([]int, error) {
	seen := map[int]struct{}{}
	var pages []int

	for _, part := range strings.Split(selection, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, apierrors.InvalidPages(selection, errors.New("пустой элемент списка"))
		}

		from, to, isRange := strings.Cut(part, "-")
		start, err := parsePageNumber(from, selection)
		if err != nil {
			return nil, err
		}
		end := start
		if isRange {
			end, err = parsePageNumber(to, selection)
			if err != nil {
				return nil, err
			}
			if end < start {
				return nil, apierrors.InvalidPages(selection, fmt.Errorf("диапазон %q идёт в обратном порядке", part))
			}
		}

		for page := start; page <= end; page++ {
			if _, exists := seen[page]; exists {
				continue
			}
			seen[page] = struct{}{}
			pages = append(pages, page)
		}
	}

	if len(pages) == 0 {
		return nil, apierrors.InvalidPages(selection, errors.New("выборка пуста"))
	}
	return pages, nil
}

func parsePageNumber(raw, selection string) (int, error) {
	number, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || number < 1 {
		return 0, apierrors.InvalidPages(selection, fmt.Errorf("номер страницы %q должен быть целым и >= 1", raw))
	}
	return number, nil
}