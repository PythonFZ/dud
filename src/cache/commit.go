package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/kevin-hanselman/duc/src/artifact"
	"github.com/kevin-hanselman/duc/src/checksum"
	"github.com/kevin-hanselman/duc/src/fsutil"
	"github.com/kevin-hanselman/duc/src/strategy"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const numWorkers = 20

// Commit calculates the checksum of the artifact, moves it to the cache, then performs a checkout.
func (ch *LocalCache) Commit(
	workingDir string,
	art *artifact.Artifact,
	strat strategy.CheckoutStrategy,
) error {
	if art.IsDir {
		return commitDirArtifact(context.Background(), ch, workingDir, art, strat)
	}
	return commitFileArtifact(ch, workingDir, art, strat)
}

func commitFileArtifact(
	ch *LocalCache,
	workingDir string,
	art *artifact.Artifact,
	strat strategy.CheckoutStrategy,
) error {
	// ignore cachePath because the artifact likely has a stale or empty checksum
	status, _, workPath, err := quickStatus(ch, workingDir, *art)
	errorPrefix := fmt.Sprintf("commit file %s", workPath)
	if err != nil {
		return errors.Wrap(err, errorPrefix)
	}
	if status.WorkspaceFileStatus == fsutil.Absent {
		return errors.Wrap(errors.Wrap(os.ErrNotExist, workPath), errorPrefix)
	}
	if status.ContentsMatch {
		return nil
	}
	if status.WorkspaceFileStatus != fsutil.RegularFile {
		return errors.Wrap(errors.New("not a regular file"), errorPrefix)
	}
	srcFile, err := os.Open(workPath)
	if err != nil {
		return errors.Wrap(err, errorPrefix)
	}
	defer srcFile.Close()

	if art.SkipCache {
		cksum, err := checksum.Checksum(srcFile, 0)
		if err != nil {
			return errors.Wrap(err, errorPrefix)
		}
		art.Checksum = cksum
		return nil
	}

	sameFs, err := fsutil.SameFilesystem(workPath, ch.Dir())
	if err != nil {
		return errors.Wrap(err, errorPrefix)
	}
	moveFile := ""
	if sameFs && strat == strategy.LinkStrategy {
		moveFile = workPath
	}

	cksum, err := ch.commitBytes(srcFile, moveFile)
	if err != nil {
		return errors.Wrap(err, errorPrefix)
	}

	art.Checksum = cksum
	// There's no need to call Checkout if using CopyStrategy; the original file still exists.
	if strat == strategy.LinkStrategy {
		// TODO: add "force" option to cache.Checkout to replace this
		exists, err := fsutil.Exists(workPath, false)
		if err != nil {
			return errors.Wrap(err, errorPrefix)
		}
		if exists {
			if err := os.Remove(workPath); err != nil {
				return errors.Wrap(err, errorPrefix)
			}
		}
		return ch.Checkout(workingDir, art, strat)
	}
	return nil
}

// commitBytes checksums the bytes from reader and results in said bytes being
// present in the cache. If moveFile is empty, commitBytes will copy from
// reader to the cache while checksumming. If moveFile is not empty, the file
// path it references is moved (i.e. renamed) to the cache after checksumming,
// thus eliminating unnecessary file IO.
func (ch *LocalCache) commitBytes(reader io.Reader, moveFile string) (string, error) {
	// If there's no file we can move, we need to copy the bytes from reader to
	// the cache.
	if moveFile == "" {
		tempFile, err := ioutil.TempFile(ch.dir, "")
		if err != nil {
			return "", err
		}
		defer tempFile.Close()
		reader = io.TeeReader(reader, tempFile)
		moveFile = tempFile.Name()
	}

	cksum, err := checksum.Checksum(reader, 0)
	if err != nil {
		return "", err
	}
	cachePath, err := ch.PathForChecksum(cksum)
	if err != nil {
		return "", err
	}
	dstDir := filepath.Dir(cachePath)
	if err = os.MkdirAll(dstDir, 0755); err != nil {
		return "", err
	}
	if err = os.Rename(moveFile, cachePath); err != nil {
		return "", err
	}
	if err := os.Chmod(cachePath, 0444); err != nil {
		return "", err
	}
	return cksum, nil
}

func commitDirManifest(ch *LocalCache, manifest *directoryManifest) (string, error) {
	// TODO: Consider using an io.Pipe() instead of a buffer.
	// For large directories this is probably more important.
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(manifest); err != nil {
		return "", err
	}
	return ch.commitBytes(buf, "")
}

func commitDirArtifact(
	ctx context.Context,
	ch *LocalCache,
	workingDir string,
	art *artifact.Artifact,
	strat strategy.CheckoutStrategy,
) error {
	// TODO: don't bother checking if regular files are up-to-date?
	status, oldManifest, err := dirArtifactStatus(ch, workingDir, *art)
	if err != nil {
		return err
	}
	if status.ContentsMatch {
		return nil
	}

	baseDir := filepath.Join(workingDir, art.Path)

	// Using this example as a reference:
	// https://godoc.org/golang.org/x/sync/errgroup#example-Group--Pipeline

	// Start a goroutine to read the directory and feed files/sub-directories
	// to the workers.
	errGroup, groupCtx := errgroup.WithContext(ctx)
	inputFiles := make(chan os.FileInfo)
	errGroup.Go(func() error {
		defer close(inputFiles)
		// We could use filepath.Walk here (as in the errgroup example), but
		// this function essentially walks the filesystem using recursion, so
		// there would be no significant benefit for the added complexity.
		// Switching to filepath.Walk may make sense if ReadDir doesn't perform
		// well on huge directories.
		infos, err := ioutil.ReadDir(baseDir)
		if err != nil {
			return err
		}
		for _, info := range infos {
			select {
			case inputFiles <- info:
			case <-groupCtx.Done():
				return groupCtx.Err()
			}
		}
		return nil
	})

	// Start workers to commit child Artifacts.
	childArtifacts := make(chan *artifact.Artifact)
	for i := 0; i < numWorkers; i++ {
		errGroup.Go(func() error {
			for info := range inputFiles {
				path := info.Name()
				// See if we can recover a sub-artifact from an existing
				// dirManifest. This enables skipping up-to-date artifacts.
				var childArt *artifact.Artifact
				childArt, ok := oldManifest.Contents[path]
				if !ok {
					childArt = &artifact.Artifact{Path: path}
				}
				if info.IsDir() {
					if !art.IsRecursive {
						continue
					}
					childArt.IsDir = true
					childArt.IsRecursive = true
					if err := commitDirArtifact(groupCtx, ch, baseDir, childArt, strat); err != nil {
						return err
					}
				} else {
					if err := commitFileArtifact(ch, baseDir, childArt, strat); err != nil {
						return err
					}
				}
				select {
				case childArtifacts <- childArt:
				case <-groupCtx.Done():
					return groupCtx.Err()
				}
			}
			return nil
		})
	}

	// Start a goroutine to close the output channel when the workers have
	// completed.
	go func() {
		errGroup.Wait()
		close(childArtifacts)
	}()

	newManifest := &directoryManifest{Path: baseDir}
	newManifest.Contents = make(map[string]*artifact.Artifact)
	for childArt := range childArtifacts {
		newManifest.Contents[childArt.Path] = childArt
	}

	// Check the group again to collect the group error.
	if err := errGroup.Wait(); err != nil {
		return err
	}

	cksum, err := commitDirManifest(ch, newManifest)
	if err != nil {
		return err
	}
	art.Checksum = cksum
	return nil
}
