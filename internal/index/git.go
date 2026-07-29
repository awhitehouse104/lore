package index

import (
	"context"

	"lore/internal/gitx"
)

type Git interface {
	IsRepository(context.Context, string) (bool, error)
	HeadOptional(context.Context, string) (string, bool, error)
	BranchState(context.Context, string) (string, bool, error)
	Changes(context.Context, string, []string) ([]gitx.Change, error)
	CommonDirectory(context.Context, string) (string, error)
	RootCommits(context.Context, string) ([]string, error)
}
