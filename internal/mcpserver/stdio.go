package mcpserver

import (
	"context"
	"io"
	"log/slog"

	"lore/internal/core"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunStdio(ctx context.Context, service *core.Service, input io.Reader, output io.Writer, logger *slog.Logger) error {
	transport := &mcp.IOTransport{
		Reader: readCloser{Reader: input},
		Writer: writeCloser{Writer: output},
	}
	return New(service, logger).Run(ctx, transport)
}

type readCloser struct {
	io.Reader
}

func (readCloser) Close() error { return nil }

type writeCloser struct {
	io.Writer
}

func (writeCloser) Close() error { return nil }
