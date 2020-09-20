package cache

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/kevin-hanselman/duc/artifact"
	"github.com/kevin-hanselman/duc/checksum"
	"github.com/kevin-hanselman/duc/fsutil"
	"github.com/pkg/errors"
)

// Status reports the status of an Artifact in the Cache.
func (ch *LocalCache) Status(workingDir string, art artifact.Artifact) (artifact.Status, error) {
	if art.IsDir {
		status, _, err := dirArtifactStatus(ch, workingDir, art)
		return status, err
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
	errorPrefix := "file status"
	if err != nil {
		return status, errors.Wrap(err, errorPrefix)
	}

	if status.WorkspaceFileStatus != fsutil.RegularFile {
		return status, nil
	}

	if art.SkipCache {
		if !status.HasChecksum {
			return status, nil
		}
		fileReader, err := os.Open(workPath)
		if err != nil {
			return status, errors.Wrap(err, errorPrefix)
		}
		workspaceFileChecksum, err := checksum.Checksum(fileReader, 0)
		if err != nil {
			return status, errors.Wrap(err, errorPrefix)
		}
		status.ContentsMatch = workspaceFileChecksum == art.Checksum
	} else {
		if !status.ChecksumInCache {
			return status, nil
		}
		status.ContentsMatch, err = fsutil.SameContents(workPath, cachePath)
		if err != nil {
			return status, errors.Wrap(err, errorPrefix)
		}
	}
	return status, nil
}

func dirArtifactStatus(
	ch *LocalCache,
	workingDir string,
	art artifact.Artifact,
) (artifact.Status, directoryManifest, error) {
	var manifest directoryManifest
	status, cachePath, workPath, err := quickStatus(ch, workingDir, art)
	if err != nil {
		return status, manifest, err
	}

	if !(status.HasChecksum && status.ChecksumInCache) {
		return status, manifest, nil
	}

	if status.WorkspaceFileStatus != fsutil.Directory {
		// TODO: Should this be an error?
		return status, manifest, nil
	}

	manifest, err = readDirManifest(cachePath)
	if err != nil {
		return status, manifest, err
	}

	// first, ensure all artifacts in the directoryManifest are up-to-date;
	// quit early if any are not.
	for _, art := range manifest.Contents {
		artStatus, err := ch.Status(workPath, *art)
		if err != nil {
			return status, manifest, err
		}
		if !artStatus.ContentsMatch {
			return status, manifest, nil
		}
	}

	// second, get a directory listing and check for untracked files;
	// quit early if any exist.
	entries, err := readDir(workPath)
	if err != nil {
		return status, manifest, err
	}
	for _, entry := range entries {
		// only check entries that don't appear in the manifest
		if _, ok := manifest.Contents[entry.Name()]; !ok {
			if entry.IsDir() {
				// if the entry is a (untracked) directory,
				// this is only a mismatch if the artifact is recursive
				if art.IsRecursive {
					return status, manifest, nil
				}
			} else {
				// if the entry is a (untracked) file,
				// this is always a mismatch
				return status, manifest, nil
			}
		}
	}
	status.ContentsMatch = true
	return status, manifest, nil
}

var readDirManifest = func(path string) (man directoryManifest, err error) {
	manifestFile, err := os.Open(path)
	if err != nil {
		return
	}
	err = json.NewDecoder(manifestFile).Decode(&man)
	return
}
