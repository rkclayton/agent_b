package tools

import (
	pathpkg "path"
	"strings"
)

func shellCommandName(value string) string {
	return strings.ToLower(strings.TrimSuffix(pathpkg.Base(strings.ReplaceAll(value, `\`, "/")), ".exe"))
}

func shellDialect(executable string) string {
	switch name := shellCommandName(executable); name {
	case "powershell", "pwsh":
		return "PowerShell"
	case "cmd":
		return "cmd.exe"
	case "":
		return "the configured shell"
	default:
		return name
	}
}

func splitShellCommands(command string) []string {
	var segments []string
	var current strings.Builder
	var quote rune
	for _, char := range command {
		switch {
		case quote != 0 && char == quote:
			quote = 0
			current.WriteRune(char)
		case quote != 0:
			current.WriteRune(char)
		case char == '\'' || char == '"':
			quote = char
			current.WriteRune(char)
		case char == ';' || char == '|' || char == '&' || char == '\n' || char == '\r':
			if strings.TrimSpace(current.String()) != "" {
				segments = append(segments, current.String())
			}
			current.Reset()
		default:
			current.WriteRune(char)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		segments = append(segments, current.String())
	}
	return segments
}

func shellPolicyTokens(command string) []string {
	var tokens []string
	for _, segment := range splitShellCommands(command) {
		tokens = append(tokens, shellWords(segment)...)
	}
	return tokens
}

func cleanShellScriptToken(value string) string {
	return strings.Trim(value, "'\"`(){}[],;")
}

func shellWords(segment string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, char := range strings.TrimSpace(segment) {
		switch {
		case quote != 0 && char == quote:
			quote = 0
		case quote == 0 && (char == '\'' || char == '"'):
			quote = char
		case quote == 0 && (char == ' ' || char == '\t' || char == '\r' || char == '\n'):
			flush()
		default:
			word.WriteRune(char)
		}
	}
	flush()
	return words
}
