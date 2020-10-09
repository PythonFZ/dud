package index

import (
	"os"
	"testing"

	"github.com/kevin-hanselman/duc/artifact"
	"github.com/kevin-hanselman/duc/stage"
)

func TestAdd(t *testing.T) {

	stageFromFileOrig := stage.FromFile
	stage.FromFile = func(path string) (stage.Stage, bool, error) {
		return stage.Stage{}, false, nil
	}
	defer func() { stage.FromFile = stageFromFileOrig }()

	t.Run("add new stage", func(t *testing.T) {
		idx := make(Index)
		path := "foo/bar.duc"

		if err := idx.AddStagesFromPaths(path); err != nil {
			t.Fatal(err)
		}

		_, added := idx[path]
		if !added {
			t.Fatal("path wasn't added to the index")
		}
	})

	t.Run("error if already tracked", func(t *testing.T) {
		idx := make(Index)
		path := "foo/bar.duc"

		var stg stage.Stage
		idx[path] = &entry{Stage: stg}

		if err := idx.AddStagesFromPaths(path); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("error if invalid stage", func(t *testing.T) {
		idx := make(Index)
		path := "foo/bar.duc"

		stage.FromFile = func(path string) (stage.Stage, bool, error) {
			return stage.Stage{}, false, os.ErrNotExist
		}

		err := idx.AddStagesFromPaths(path)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("error if new stage declares already owned outputs", func(t *testing.T) {
		stage.FromFile = func(path string) (stage.Stage, bool, error) {
			stg := stage.Stage{
				Outputs: map[string]*artifact.Artifact{
					"subDir/foo.bin": {Path: "subDir/foo.bin"},
				},
			}
			return stg, false, nil
		}
		idx := make(Index)
		idx["foo.yaml"] = &entry{
			Stage: stage.Stage{
				WorkingDir: "subDir",
				Outputs: map[string]*artifact.Artifact{
					"foo.bin": {Path: "foo.bin"},
				},
			},
		}
		err := idx.AddStagesFromPaths("bar.yaml")
		if err == nil {
			t.Fatal("expected error")
		}
		expectedError := "add stage bar.yaml: artifact subDir/foo.bin is already owned by foo.yaml"
		if err.Error() != expectedError {
			t.Fatalf("\nerror want: %s\nerror got: %s", expectedError, err.Error())
		}
	})
}
