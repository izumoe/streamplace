package app

import (
	"embed"
	"encoding/json"
	"io/fs"
)

//go:embed all:dist/**
var AllFiles embed.FS

//go:embed all:assets/**
var AssetFiles embed.FS

//go:embed package.json
var pkg []byte

// Files returns the native update bundles and shared static assets.
func Files() (fs.FS, error) {
	rootFiles, err := fs.Sub(AllFiles, "dist")
	if err != nil {
		return nil, err
	}
	return rootFiles, nil
}

// fetch assets including fonts
func Assets() (fs.FS, error) {
	assetFiles, err := fs.Sub(AssetFiles, "assets")
	if err != nil {
		return nil, err
	}
	return assetFiles, nil
}

func PackageJSON() (map[string]any, error) {
	var data map[string]any
	err := json.Unmarshal(pkg, &data)
	if err != nil {
		return nil, err
	}
	return data, nil
}
