// Package mapping translates RetroArch WebDAV paths to/from RomM lookups.
package mapping

import (
	"strings"

	"github.com/jleight/retroarch-romm-bridge/internal/romm"
)

// ManifestKey is the special file RetroArch reads/writes to track server state.
const ManifestKey = "manifest.server"

// Key is a parsed RetroArch sync path.
type Key struct {
	Kind       romm.AssetKind // KindSave or KindState
	ContentDir string         // immediate parent dir (platform hint); "" if flat
	FileName   string         // e.g. "Pokemon Emerald.srm"
	Stem       string         // FileName without its final extension (the ROM basename)
	Ext        string         // final extension, e.g. "srm", "state", "state1"
}

// ParseResult classifies a request path.
type ParseResult int

const (
	// ResultUnknown is a path the bridge does not handle (other sync folders,
	// stray paths). Callers should respond benignly (404 on GET, 201 no-op).
	ResultUnknown ParseResult = iota
	// ResultManifest is the manifest.server file.
	ResultManifest
	// ResultAsset is a save or state file (see Key).
	ResultAsset
)

// Parse interprets a decoded URL path (with or without leading slash).
// RetroArch keys look like "saves/<dir>/<game>.srm" or "states/<game>.state1";
// with content-directory sorting on, <dir> is the platform folder.
func Parse(path string) (ParseResult, Key) {
	p := strings.Trim(path, "/")
	if p == "" {
		return ResultUnknown, Key{}
	}
	if p == ManifestKey {
		return ResultManifest, Key{}
	}

	segs := strings.Split(p, "/")
	var kind romm.AssetKind
	switch segs[0] {
	case "saves":
		kind = romm.KindSave
	case "states":
		kind = romm.KindState
	default:
		// configs/thumbnails/system or anything else — not handled.
		return ResultUnknown, Key{}
	}

	rest := segs[1:]
	if len(rest) == 0 {
		return ResultUnknown, Key{} // bare "saves" / "states" dir
	}
	fileName := rest[len(rest)-1]
	if fileName == "" {
		return ResultUnknown, Key{} // trailing slash: a directory, not a file
	}

	contentDir := ""
	if len(rest) >= 2 {
		// immediate parent of the file is the content directory (platform hint)
		contentDir = rest[len(rest)-2]
	}

	stem, ext := splitLastExt(fileName)
	return ResultAsset, Key{
		Kind:       kind,
		ContentDir: contentDir,
		FileName:   fileName,
		Stem:       stem,
		Ext:        ext,
	}
}

// splitLastExt splits the final ".ext" from a filename, leaving the ROM
// basename (e.g. "Game v1.1 (USA).srm" -> "Game v1.1 (USA)", "srm").
func splitLastExt(name string) (stem, ext string) {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// CollectionPath joins the collection ("saves"/"states") + optional content-dir
// folder + filename into a RetroArch key, e.g. "saves/gba/Pokemon.srm".
func CollectionPath(kind romm.AssetKind, folder, fileName string) string {
	collection := "saves"
	if kind == romm.KindState {
		collection = "states"
	}
	if folder == "" {
		return collection + "/" + fileName
	}
	return collection + "/" + folder + "/" + fileName
}

// AssetKey builds a RetroArch key from a basename + extension under folder.
func AssetKey(kind romm.AssetKind, folder, basename, ext string) string {
	if ext != "" {
		basename += "." + ext
	}
	return CollectionPath(kind, folder, basename)
}
