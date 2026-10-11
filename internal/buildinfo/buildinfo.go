// Package buildinfo хранит сведения о сборке бинарника.
//
// Значения проставляются линкером при сборке:
//
//	go build -ldflags "-X barcodereader/internal/buildinfo.Version=1.0.0"
//
// Без них остаются пустыми, и в журнал попадает "dev". Это осознанно:
// по журналю службы должно быть видно, какой именно файл запущен.
// Служба часто поднимает бинарник из Program Files, тогда как разработчик
// собирает в bin/, — и расхождение версий стоит дорогих часов диагностики.
package buildinfo

// Проставляются через -ldflags.
var (
	// Version — семантическая версия.
	Version = ""
	// Revision — короткий хеш коммита.
	Revision = ""
	// BuildTime — время сборки в RFC3339.
	BuildTime = ""
)

// String возвращает краткое описание сборки для журнала.
func String() string {
	version := Version
	if version == "" {
		version = "dev"
	}
	if Revision == "" {
		return version
	}
	return version + "+" + Revision
}