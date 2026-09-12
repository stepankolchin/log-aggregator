// Пакет web экспортирует встроенный HTML-файл дашборда.
// //go:embed встраивает файл в бинарь во время компиляции — отдельный сервер статики не нужен.
package web

import _ "embed"

//go:embed index.html
var IndexHTML []byte
