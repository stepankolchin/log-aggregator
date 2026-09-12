package router

import (
	"regexp"
	"strings"

	"github.com/stepankolchin/log-aggregator/internal/config"
	"github.com/stepankolchin/log-aggregator/internal/model"
)

// rule — скомпилированное правило маршрутизации.
type rule struct {
	name         string
	service      string         // точное совпадение (пусто = любой)
	levels       map[string]bool // допустимые уровни (пусто = любой)
	messageRegex *regexp.Regexp  // регулярное выражение на message (nil = любой)
	sinks        []string
}

// buildRules компилирует правила из конфига.
// Regexp компилируется один раз при старте — не при каждом логе.
func buildRules(routes []config.RouteConfig) ([]rule, error) {
	rules := make([]rule, 0, len(routes))
	for _, r := range routes {
		compiled := rule{
			name:    r.Name,
			service: r.Match.Service,
			sinks:   r.Sinks,
		}

		// Собираем множество уровней: поле level имеет приоритет над levels
		if r.Match.Level != "" {
			compiled.levels = map[string]bool{r.Match.Level: true}
		} else if len(r.Match.Levels) > 0 {
			compiled.levels = make(map[string]bool, len(r.Match.Levels))
			for _, l := range r.Match.Levels {
				compiled.levels[strings.ToLower(l)] = true
			}
		}

		if r.Match.MessageRegex != "" {
			re, err := regexp.Compile(r.Match.MessageRegex)
			if err != nil {
				return nil, err
			}
			compiled.messageRegex = re
		}

		rules = append(rules, compiled)
	}
	return rules, nil
}

// matches возвращает true, если лог подходит под данное правило.
func (r *rule) matches(entry model.LogEntry) bool {
	if r.service != "" && r.service != entry.Service {
		return false
	}
	if len(r.levels) > 0 && !r.levels[entry.Level] {
		return false
	}
	if r.messageRegex != nil && !r.messageRegex.MatchString(entry.Message) {
		return false
	}
	return true
}
