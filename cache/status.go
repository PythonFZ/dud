package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kevlar1818/duc/artifact"
	"github.com/kevlar1818/duc/fsutil"
	"github.com/pkg/errors"
)

// Status reports the status of an Artifact in the Cache.
func (ch *LocalCache) Status(workingDir string, art artifact.Artifact) (artifact.Status, error) {
	if art.IsDir {
		return dirArtifactStatus(ch, workingDir, art)
	}
	return fileArtifactStatus(ch, workingDir, art)
}

// quickStatus populates all artifact.Status fields except for ContentsMatch.
// However, this function will set ContentsMatch if the workspace file is
// a link and the other status booleans are true; checking to see if a link
// points to the cache is, as this function suggests, quick.
var quickStatus = func(
	// TODO: It may be worth exposing this version of status (bypassing the full
	// status check) using a CLI flag
	ch *LocalCache,
	workingDir string,
	art artifact.Artifact,
) (status artifact.Status, cachePath, workPath string, err error) {
	workPath = filepath.Join(workingDir, art.Path)
	cachePath, err = ch.PathForChecksum(art.Checksum)
	if err != nil { // An error means the checksum is invalid
		status.HasChecksum = false
	} else {
		status.HasChecksum = true
		status.ChecksumInCache, err = fsutil.Exists(cachePath, false) // TODO: check for regular file?
		if err != nil {
			return
		}
	}
	status.WorkspaceFileStatus, err = fsutil.FileStatusFromPath(workPath)
	if err != nil {
		return
	}
	if status.HasChecksum && status.ChecksumInCache && status.WorkspaceFileStatus == fsutil.Link {
		var linkDst string
		linkDst, err = os.Readlink(workPath)
		if err != nil {
			return
		}
		status.ContentsMatch = linkDst == cachePath
	}
	return
}

var fileArtifactStatus = func(ch *LocalCache, workingDir string, art artifact.Artifact) (artifact.Status, error) {
	status, cachePath, workPath, err := quickStatus(ch, workingDir, art)
	if err != nil {
		return status, errors.Wrap(err, "fileStatus")
	}

	if !status.ChecksumInCache {
		return status, nil
	}

	if status.WorkspaceFileStatus == fsutil.RegularFile {
		status.ContentsMatch, err = fsutil.SameContents(workPath, cachePath)
		if err != nil {
			return status, errors.Wrap(err, "fileStatus")
		}
	}
	return status, nil
}

func dirArtifactStatus(ch *LocalCache, workingDir string, art artifact.Artifact) (artifact.Status, error) {
	status, cachePath, workPath, err := quickStatus(ch, workingDir, art)
	if err != nil {
		return status, err
	}

	if !(status.HasChecksum && status.ChecksumInCache) {
		return status, nil
	}

	if status.WorkspaceFileStatus != fsutil.Directory {
		// TODO: Should this be an error?
		return status, fmt.Errorf("dir status: %#v is not a directory", workPath)
	}

	dirManifest, err := readDirManifest(cachePath)
	if err != nil {
		return status, err
	}

	// first, ensure all artifacts in the directoryManifest are up-to-date;
	// quit early if any are not.
	manifestPaths := make(map[string]bool)
	for _, art := range dirManifest.Contents {
		manifestPaths[art.Path] = true
		artStatus, err := ch.Status(workPath, *art)
		if err != nil {
			return status, err
		}
		if !artStatus.ContentsMatch {
			return status, nil
		}
	}

	// second, get a directory listing and check for untracked files;
	// quit early if any exist.
	entries, err := readDir(workPath)
	if err != nil {
		return status, err
	}
	for _, entry := range entries {
		// only check entries that don't appear in the manifest
		if !manifestPaths[entry.Name()] {
			if entry.IsDir() {
				// if the entry is a (untracked) directory,
				// this is only a mismatch if the artifact is recursive
				if art.IsRecursive {
					return status, nil
				}
			} else {
				// if the entry is a (untracked) file,
				// this is always a mismatch
				return status, nil
			}
		}
	}
	status.ContentsMatch = true
	return status, nil
}

var readDirManifest = func(path string) (man directoryManifest, err error) {
	manifestFile, err := os.Open(path)
	if err != nil {
		return
	}
	err = json.NewDecoder(manifestFile).Decode(&man)
	return
}
