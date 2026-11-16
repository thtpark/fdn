package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thtpark/fdn/internal/distribution"
)

func packageSource(ctx context.Context, version, commit string) error {
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	out := filepath.Join("dist", "source")
	release, err := distribution.CreateSourceAssets(ctx, ".", out, version, commit)
	if err != nil {
		return err
	}
	names := distribution.SourceAssetNames(version)
	digest, err := distribution.SHA256(filepath.Join(out, names[3]))
	if err != nil {
		return err
	}
	fmt.Printf("source tree: %s\nsource checksum manifest digest: %s\n", release.Tree, digest)
	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		file, err := os.OpenFile(summary, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintf(file, "### Source companions\n\nCommit: `%s`\n\nTree: `%s`\n\nSource checksum manifest SHA-256: `%s`\n\nUnsigned/unattested. %s\n",
			commit, release.Tree, digest, release.OfflineDependencyLimitation)
		return errors.Join(writeErr, file.Close())
	}
	return nil
}
