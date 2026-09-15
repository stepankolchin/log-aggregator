// Пакет web экспортирует встроенный HTML-файл дашборда, стили и клиентский скрипт.
// //go:embed встраивает файлы в бинарь во время компиляции — отдельный сервер статики не нужен.
package web

import _ "embed"

//go:embed index.html
var IndexHTML []byte

//go:embed style.css
var StyleCSS []byte

//go:embed app.js
var AppJS []byte

//go:embed swagger.html
var SwaggerHTML []byte

//go:embed openapi.yaml
var OpenAPIYAML []byte

//go:embed openapi.json
var OpenAPIJSON []byte
