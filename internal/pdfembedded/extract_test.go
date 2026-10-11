package pdfembedded

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Регрессия на падение стадии A в Docker.
//
// pdfcpu ищет свой конфиг в пользовательском каталоге и создаёт его, если
// нет. В образе scratch под пользователем без домашнего каталога это давало
// «config problem: mkdir /.config: permission denied», а pdfcpu на это ещё
// и паникует через fault.Fail. Стадия A молча выпадала на каждом запросе.
func TestExtractWorksWithoutWritableHome(t *testing.T) {
	// Каталог, доступный только на чтение, — как / в контейнере.
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Skipf("не удалось снять права: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	t.Setenv("HOME", readOnly)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(readOnly, ".config"))

	pdf := readFixture(t, "qr_embedded_png.pdf")

	images, err := Extract(context.Background(), pdf, nil, 5)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("стадия A не извлекла ни одной картинки")
	}
}

func TestPageCountWorksWithoutWritableHome(t *testing.T) {
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Skipf("не удалось снять права: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	t.Setenv("HOME", readOnly)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(readOnly, ".config"))

	pdf := readFixture(t, "qr_multipage.pdf")

	count, err := PageCount(context.Background(), pdf)
	if err != nil {
		t.Fatalf("PageCount: %v", err)
	}
	if count < 2 {
		t.Errorf("PageCount = %d, ожидалось минимум 2", count)
	}
}

// В конфигурации не должно быть включений, тянущих запись на диск.
func TestConfigurationIsStateless(t *testing.T) {
	conf := newConfiguration()

	if conf == nil {
		t.Fatal("newConfiguration вернул nil")
	}
	if conf.UnsupportedResourcePolicy != model.UnsupportedResourceSkip {
		t.Errorf("UnsupportedResourcePolicy = %v, ожидался Skip",
			conf.UnsupportedResourcePolicy)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", name)
	pdf, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("фикстура %s недоступна: %v", name, err)
	}
	return pdf
}