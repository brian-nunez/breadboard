// Package starters lists starters and renders their templates. Applying a
// starter to a project (writing files, editing experiment.yaml) is a core
// operation; this package only knows what each starter contains.
package starters

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"text/template"

	templates "github.com/brian-nunez/breadboard/starters"
)

// Toolchain versions written into every Go starter.
const (
	GoVersion     = "1.27.4"
	GoImage       = "1.27.4-alpine3.24"
	AlpineVersion = "3.24.4"
)

// Starter is one starter's definition.
type Starter struct {
	ID       string
	Title    string
	Summary  string
	Language string
	// Template is the directory in the embedded templates.
	Template string
	// Dir is the default directory the code is written to, relative to the project.
	Dir string
	// Requires lists catalog types the starter needs; missing ones are added.
	Requires []string
	Apps     []App
}

// App is an app service the starter adds.
type App struct {
	Name    string
	Command []string
	// Uses lists catalog types; each resolves to the experiment's service of that type.
	Uses []string
}

var all = []Starter{
	{
		ID:       "kafka/producer-consumer",
		Title:    "Kafka producer and consumer",
		Summary:  "Topics, partitions, keys, consumer groups, offsets, rebalancing.",
		Language: "go",
		Template: "go/kafka/producer-consumer",
		Dir:      "producer-consumer",
		Requires: []string{"kafka"},
		Apps: []App{
			{Name: "producer", Command: []string{"go", "run", "./cmd/producer"}, Uses: []string{"kafka"}},
			{Name: "consumer", Command: []string{"go", "run", "./cmd/consumer"}, Uses: []string{"kafka"}},
		},
	},
	{
		ID:       "kafka/sink-to-postgres",
		Title:    "Kafka sink to Postgres",
		Summary:  "Consume, transform, write; idempotent inserts; at-least-once delivery; migrations.",
		Language: "go",
		Template: "go/kafka/sink-to-postgres",
		Dir:      "sink-to-postgres",
		Requires: []string{"kafka", "postgres"},
		Apps: []App{
			{Name: "sink-producer", Command: []string{"go", "run", "./cmd/sink-producer"}, Uses: []string{"kafka"}},
			{Name: "sink", Command: []string{"go", "run", "./cmd/sink"}, Uses: []string{"kafka", "postgres"}},
		},
	},
}

// All returns every starter.
func All() []Starter { return slices.Clone(all) }

// Lookup finds a starter by id.
func Lookup(id string) (Starter, bool) {
	i := slices.IndexFunc(all, func(s Starter) bool { return s.ID == id })
	if i < 0 {
		return Starter{}, false
	}
	return all[i], true
}

// Data is what templates can use.
type Data struct {
	Experiment    string
	GoVersion     string
	GoImage       string
	AlpineVersion string
}

// File is one rendered file, relative to the starter's directory.
type File struct {
	Path string
	Data []byte
}

// Render renders every template file of s.
func Render(s Starter, experiment string) ([]File, error) {
	data := Data{Experiment: experiment, GoVersion: GoVersion, GoImage: GoImage, AlpineVersion: AlpineVersion}
	var files []File
	err := fs.WalkDir(templates.FS, s.Template, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".tmpl") {
			return fmt.Errorf("starter %s: %s is missing the .tmpl suffix", s.ID, p)
		}
		src, err := fs.ReadFile(templates.FS, p)
		if err != nil {
			return err
		}
		t, err := template.New(path.Base(p)).Option("missingkey=error").Parse(string(src))
		if err != nil {
			return fmt.Errorf("starter %s: %w", s.ID, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return fmt.Errorf("starter %s: %w", s.ID, err)
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p, s.Template+"/"), ".tmpl")
		files = append(files, File{Path: rel, Data: buf.Bytes()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
