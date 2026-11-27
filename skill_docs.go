package productdocs

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed docs/CONNECTORS.md docs/SKILLS.md docs/INSTALL.md
var shipped embed.FS

// AgentHelp extracts the short operator instructions from the same documents
// that ship beside the application.
func AgentHelp(source fs.FS) (string, error) {
	if source == nil {
		source = shipped
	}
	sections := []struct{ file, title string }{
		{"CONNECTORS.md", "Credentials"},
		{"CONNECTORS.md", "Connectors"},
		{"SKILLS.md", "Skills"},
		{"SKILLS.md", "Scheduled jobs"},
		{"SKILLS.md", "Notifications"},
		{"INSTALL.md", "Settings pages"},
	}
	var body strings.Builder
	for _, section := range sections {
		data, err := fs.ReadFile(source, "docs/"+section.file)
		if err != nil {
			return "", err
		}
		marker := "<!-- agent-help: " + section.title + " -->"
		_, tail, started := strings.Cut(string(data), marker)
		text, _, ended := strings.Cut(tail, "<!-- /agent-help -->")
		text = strings.TrimSpace(text)
		if !started || !ended || text == "" || len(text) > 1600 || strings.Contains(text, "<!-- agent-help:") {
			return "", fmt.Errorf("%s has no short %s help section", section.file, section.title)
		}
		fmt.Fprintf(&body, "\n\n## %s\n\n%s", section.title, text)
	}
	return body.String() + "\n", nil
}
