package markdownlink

import "strings"

// Destinations extracts inline and reference-definition destinations from one
// Markdown line. It intentionally mirrors Lore's bounded lint parser rather
// than attempting to interpret arbitrary HTML or construct a Markdown AST.
func Destinations(line string) []string {
	var destinations []string
	for searchFrom := 0; searchFrom < len(line); {
		relative := strings.Index(line[searchFrom:], "](")
		if relative < 0 {
			break
		}
		open := searchFrom + relative + 1
		start := open + 1
		depth := 1
		escaped := false
		closeIndex := -1
		for index := start; index < len(line); index++ {
			value := line[index]
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			switch value {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					closeIndex = index
				}
			}
			if closeIndex >= 0 {
				break
			}
		}
		if closeIndex < 0 {
			break
		}
		destination := extractDestination(strings.TrimSpace(line[start:closeIndex]))
		destinations = append(destinations, destination)
		searchFrom = closeIndex + 1
	}
	if destination, ok := referenceDefinitionDestination(line); ok {
		destinations = append(destinations, destination)
	}
	return destinations
}

func referenceDefinitionDestination(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	end := strings.Index(trimmed, "]:")
	if end <= 1 {
		return "", false
	}
	raw := strings.TrimSpace(trimmed[end+2:])
	if raw == "" {
		return "", false
	}
	return extractDestination(raw), true
}

func extractDestination(raw string) string {
	destination := raw
	if strings.HasPrefix(raw, "<") {
		if end := strings.Index(raw, ">"); end >= 0 {
			destination = raw[1:end]
		}
	} else if end := strings.IndexAny(raw, " \t"); end >= 0 {
		destination = raw[:end]
	}
	return unescapeDestination(destination)
}

func unescapeDestination(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	escaped := false
	for _, r := range value {
		if escaped {
			builder.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		builder.WriteRune(r)
	}
	if escaped {
		builder.WriteRune('\\')
	}
	return builder.String()
}
