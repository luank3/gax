package app

import (
	"io"
	"strings"

	"github.com/luank3/gax/internal/config"
	terminalmarkdown "github.com/luank3/gax/internal/terminal/markdown"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

func writeAnswer(writer io.Writer, answer string) error {
	if answer == "" {
		return nil
	}
	if _, err := io.WriteString(writer, answer); err != nil {
		return err
	}
	if !strings.HasSuffix(answer, "\n") {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

func writeAssistantAnswer(writer io.Writer, answer string, store *config.Store) error {
	columns, terminal := terminalwidth.WriterColumns(writer)
	return writeAssistantAnswerWithTerminal(writer, answer, store, columns, terminal)
}

func writeAssistantAnswerWithTerminal(writer io.Writer, answer string, store *config.Store, physicalColumns int, terminal bool) error {
	if answer == "" {
		return nil
	}
	if !terminal || store == nil || !store.Bool("markdown") {
		return writeAnswer(writer, answer)
	}
	columns := markdownDisplayColumns(store, physicalColumns)
	if columns < 1 {
		columns = 1
	}
	rendered := terminalmarkdown.Render(answer, columns, true)
	if _, err := io.WriteString(writer, rendered); err != nil {
		return err
	}
	if !terminalmarkdown.HasTrailingNewline(rendered) {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

func stringPtr(value string) *string { return &value }

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
