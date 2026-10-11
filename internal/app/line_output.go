package app

import (
	"io"

	"github.com/luank3/gax/internal/diag"
)

func writeWarningDiagnostic(writer io.Writer, format string, values ...any) {
	diag.Warn(writer, format, values...)
}

type lineOutputWriter struct {
	output           io.Writer
	trailingNewlines int
}

func newLineOutputWriter(output io.Writer, trailingNewlines int) *lineOutputWriter {
	return &lineOutputWriter{output: output, trailingNewlines: trailingNewlines}
}

func (writer *lineOutputWriter) Write(data []byte) (int, error) {
	written, err := writer.output.Write(data)
	if written > 0 {
		suffix := 0
		for index := written - 1; index >= 0 && data[index] == '\n'; index-- {
			suffix++
		}
		if suffix == written {
			writer.trailingNewlines += suffix
		} else {
			writer.trailingNewlines = suffix
		}
	}
	return written, err
}

func (writer *lineOutputWriter) BlockSeparator() {
	for writer.trailingNewlines < 2 {
		_, _ = writer.Write([]byte{'\n'})
	}
}

func (writer *lineOutputWriter) SyncExternalLine() {
	writer.trailingNewlines = 1
}

func syncLineOutput(output io.Writer) {
	if writer, ok := output.(*lineOutputWriter); ok {
		writer.SyncExternalLine()
	}
}
