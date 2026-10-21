package distribution

import (
	"encoding/json"
	"io/fs"
	"path"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/domain"
)

func GuideEntries(prefix string) ([]Entry, error) {
	if !fs.ValidPath(prefix) || prefix == "." {
		return nil, domain.Fail(domain.Invalid, "release guide prefix must be a relative directory")
	}
	catalog, err := guidance.Catalog()
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	for _, file := range catalog.Files {
		data, err := guidance.Read(file.Path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Name: path.Join(prefix, file.Path), Data: data, Mode: 0644})
	}
	manifest, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(entries, Entry{Name: path.Join(prefix, "MANIFEST.json"), Data: manifest, Mode: 0644}), nil
}
