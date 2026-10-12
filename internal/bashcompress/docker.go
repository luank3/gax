package bashcompress

import (
	"fmt"
	"strings"
)

// dockerColumns are the fields of the default `docker ps` table.
var dockerColumns = []string{"CONTAINER ID", "IMAGE", "COMMAND", "CREATED", "STATUS", "PORTS", "NAMES"}

// filterDocker compacts `docker ps` into the fields rtk keeps: id, name, image, status, ports.
// rtk re-runs docker with a `--format` template; gax reads the default table instead, which pads
// every column with runs of two spaces.
func filterDocker(invocation Invocation, output string, exitCode int) (Result, bool) {
	if invocation.subcommand() != "ps" {
		return Result{}, false
	}
	if exitCode != 0 {
		return Result{}, false
	}
	lines := splitLines(output)
	if len(lines) == 0 {
		return Result{}, false
	}
	header, ok := parseDockerHeader(lines[0])
	if !ok {
		return Result{}, false
	}
	index := make(map[string]int, len(header))
	for position, name := range header {
		index[name] = position
	}
	containers := make([]string, 0, len(lines))
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		cells := dockerCells(line)
		switch {
		case len(cells) == len(header):
		case len(cells) == len(header)-1:
			// An empty cell leaves one fewer run of spaces: the ports column is the one docker
			// pads blank when a container publishes nothing.
			ports := index["PORTS"]
			cells = append(cells, "")
			copy(cells[ports+1:], cells[ports:])
			cells[ports] = ""
		default:
			return Result{}, false
		}
		value := func(name string) string {
			position := index[name]
			if position >= len(cells) {
				return ""
			}
			return cells[position]
		}
		id := value("CONTAINER ID")
		name := value("NAMES")
		if id == "" || name == "" {
			return Result{}, false
		}
		if len(id) > 12 {
			id = id[:12]
		}
		image := value("IMAGE")
		if slash := strings.LastIndex(image, "/"); slash >= 0 {
			image = image[slash+1:]
		}
		suffix := ""
		if ports := compactPorts(value("PORTS")); ports != "" {
			suffix = " [" + ports + "]"
		}
		containers = append(containers, fmt.Sprintf("  %s %s (%s) %s%s", id, name, image, value("STATUS"), suffix))
	}
	total := len(containers)
	if len(containers) > capList {
		containers = containers[:capList]
	}
	var text strings.Builder
	fmt.Fprintf(&text, "[docker] %d containers:\n", total)
	for _, container := range containers {
		text.WriteString(container + "\n")
	}
	if total > len(containers) {
		fmt.Fprintf(&text, "  … +%d more\n", total-len(containers))
	}
	// The COMMAND and CREATED columns are not in the compact form, so the raw table is named as
	// the recovery path even when no container was capped.
	return Result{Text: trimTrailingBlankLines(text.String()), Omitted: true}, true
}

// dockerCells splits a `docker ps` row into its cells at runs of two or more spaces, which is how
// the table pads its columns.
func dockerCells(line string) []string {
	cells := make([]string, 0, len(dockerColumns))
	start := 0
	index := 0
	for index < len(line) {
		if line[index] != ' ' {
			index++
			continue
		}
		run := 0
		for index+run < len(line) && line[index+run] == ' ' {
			run++
		}
		if run >= 2 {
			if cell := strings.TrimSpace(line[start:index]); cell != "" {
				cells = append(cells, cell)
			}
			index += run
			start = index
			continue
		}
		index += run
	}
	if cell := strings.TrimSpace(line[start:]); cell != "" {
		cells = append(cells, cell)
	}
	return cells
}

// parseDockerHeader returns the column names of a `docker ps` header, or false when the table is
// not the default one (a `--format` template names its own columns).
func parseDockerHeader(line string) ([]string, bool) {
	header := dockerCells(line)
	if len(header) < 4 {
		return nil, false
	}
	present := make(map[string]bool, len(header))
	for _, name := range header {
		present[name] = true
	}
	for _, required := range []string{"CONTAINER ID", "IMAGE", "STATUS", "NAMES"} {
		if !present[required] {
			return nil, false
		}
	}
	return header, true
}

// compactPorts trims a port list to its mappings, dropping rtk's "-" placeholder.
func compactPorts(value string) string {
	if value == "" || value == "-" {
		return ""
	}
	return value
}
