package distribution

import (
	"bytes"
	"errors"
	"path"
	"strconv"
	"strings"
	"text/scanner"
)

func checkSourceModule(data []byte) error {
	var tokens scanner.Scanner
	tokens.Init(bytes.NewReader(data))
	tokens.Mode = scanner.ScanStrings | scanner.ScanRawStrings | scanner.ScanComments | scanner.SkipComments
	tokens.Error = func(*scanner.Scanner, string) {}
	equal := false
	for token := tokens.Scan(); token != scanner.EOF; token = tokens.Scan() {
		if equal && token == '>' {
			token = tokens.Scan()
			if token == scanner.EOF {
				return errors.New("source module replacement is incomplete")
			}
			target := tokens.TokenText()
			if token == scanner.String || token == scanner.RawString {
				var err error
				target, err = strconv.Unquote(target)
				if err != nil {
					return errors.New("invalid source module replacement")
				}
			} else {
				line, _, _ := strings.Cut(string(data[tokens.Position.Offset:]), "\n")
				fields := strings.Fields(line)
				if len(fields) == 0 {
					return errors.New("source module replacement is incomplete")
				}
				target = fields[0]
			}
			if target == "." || target == ".." || strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") ||
				strings.HasPrefix(target, "/") || strings.Contains(target, "\\") || (len(target) >= 2 && target[1] == ':') {
				return errors.New("filesystem Go module replacements cannot certify complete tracked source inputs")
			}
		}
		equal = token == '='
	}
	return nil
}

func checkSourceLinks(files []sourceFile) error {
	entries := map[string]sourceFile{}
	directories := map[string]bool{".": true}
	for _, file := range files {
		entries[file.name] = file
		for directory := path.Dir(file.name); directory != "."; directory = path.Dir(directory) {
			directories[directory] = true
		}
	}
	for _, file := range files {
		if file.mode != "120000" {
			continue
		}
		current := []string{}
		if parent := path.Dir(file.name); parent != "." {
			current = strings.Split(parent, "/")
		}
		pending := strings.Split(string(file.data), "/")
		hops := 1
		for len(pending) != 0 {
			component := pending[0]
			pending = pending[1:]
			switch component {
			case "", ".":
				continue
			case "..":
				if len(current) == 0 {
					return errors.New("source symlink resolution escapes the release tree")
				}
				current = current[:len(current)-1]
				continue
			}
			current = append(current, component)
			name := strings.Join(current, "/")
			target, exists := entries[name]
			if exists && target.mode == "120000" {
				hops++
				if hops > 40 {
					return errors.New("source symlink cycle or excessive link depth")
				}
				current = current[:len(current)-1]
				pending = append(strings.Split(string(target.data), "/"), pending...)
			} else if !directories[name] && (!exists || len(pending) != 0) {
				return errors.New("source symlink target is not resolved solely by tracked release inputs")
			}
		}
	}
	return nil
}
