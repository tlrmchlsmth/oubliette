package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/tlrmchlsmth/oubliette/internal/evidence"
)

func main() {
	var storeRoot, runPath, artifactsRoot string
	flag.StringVar(&storeRoot, "store", "", "operator-owned evidence store directory")
	flag.StringVar(&runPath, "run", "", "JSON file containing evidence run metadata")
	flag.StringVar(&artifactsRoot, "artifacts", "", "directory containing redacted run artifacts")
	flag.Parse()
	if storeRoot == "" || runPath == "" || artifactsRoot == "" {
		log.Fatal("store, run, and artifacts are required")
	}
	runBytes, err := os.ReadFile(runPath)
	if err != nil {
		log.Fatal(err)
	}
	var run evidence.Run
	if err := json.Unmarshal(runBytes, &run); err != nil {
		log.Fatal(err)
	}
	inputs, err := readArtifacts(artifactsRoot)
	if err != nil {
		log.Fatal(err)
	}
	bundle, err := evidence.BuildRunBundle(run, inputs)
	if err != nil {
		log.Fatal(err)
	}
	location, err := (evidence.DirectoryStore{Root: storeRoot}).Put(context.Background(), bundle)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\t%s\n", bundle.Manifest.BundleDigest, location)
}

func readArtifacts(root string) ([]evidence.InputArtifact, error) {
	var inputs []evidence.InputArtifact
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("evidence artifact %q is not a regular file", name)
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		mediaType := mime.TypeByExtension(filepath.Ext(name))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		inputs = append(inputs, evidence.InputArtifact{Name: filepath.ToSlash(relative), MediaType: strings.Split(mediaType, ";")[0], Data: data})
		return nil
	})
	return inputs, err
}
