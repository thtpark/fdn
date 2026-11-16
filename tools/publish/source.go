package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/thtpark/fdn/internal/distribution"
)

func publishAssets(ctx context.Context, root, input, output, version, commit string) error {
	source := filepath.Join(input, "fdn-source")
	if _, err := distribution.VerifySourceAssets(ctx, root, source, version, commit); err != nil {
		return err
	}
	if err := bundle(input, output, version, commit); err != nil {
		return err
	}
	stage := filepath.Join(output, ".source")
	if err := os.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	names := distribution.SourceAssetNames(version)
	for _, name := range names {
		if err := copySourceAsset(filepath.Join(source, name), filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	// Verify the bytes to be published, not just inputs that might have changed
	// between verification and copying.
	if _, err := distribution.VerifySourceAssets(ctx, root, stage, version, commit); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Link(filepath.Join(stage, name), filepath.Join(output, name)); err != nil {
			return err
		}
	}
	directory, err := os.Open(output)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func copySourceAsset(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("source publication inputs must be regular files")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		input.Close()
		return errors.New("source publication input was replaced")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, input.Close(), output.Sync(), output.Close())
}
