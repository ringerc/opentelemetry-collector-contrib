// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"

	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
)

const unmaintainedStatus = "unmaintained"

type generator interface {
	generate(data *githubData) error
}

// Generates files specific to Github or Dependabot according to status metadata:
// .github/CODEOWNERS
// .github/ALLOWLIST
// .github/dependabot.yml
// .github/ISSUE_TEMPLATES/*.yaml (list of components)
func main() {
	folder := flag.String("folder", ".", "folder investigated for codeowners")
	allowlistFilePath := flag.String("allowlist", "cmd/githubgen/allowlist.txt", "path to a file containing an allowlist of members outside the OpenTelemetry organization")
	flag.Parse()
	var generators []generator
	for _, arg := range flag.Args() {
		switch arg {
		case "issue-templates":
			generators = append(generators, issueTemplatesGenerator{})
		case "dependabot":
			generators = append(generators, dependabotGenerator{})
		case "codeowners":
			generators = append(generators, codeownersGenerator{})
		default:
			panic(fmt.Sprintf("Unknown generator: %s", arg))
		}
	}
	if len(generators) == 0 {
		generators = []generator{issueTemplatesGenerator{}, dependabotGenerator{}, codeownersGenerator{}}
	}
	if err := run(*folder, *allowlistFilePath, generators); err != nil {
		log.Fatal(err)
	}
}

type codeowners struct {
	// Active codeowners
	Active []string `mapstructure:"active"`
	// Emeritus codeowners
	Emeritus []string `mapstructure:"emeritus"`
}

type Status struct {
	Stability     map[string][]string `mapstructure:"stability"`
	Distributions []string            `mapstructure:"distributions"`
	Class         string              `mapstructure:"class"`
	Warnings      []string            `mapstructure:"warnings"`
	Codeowners    *codeowners         `mapstructure:"codeowners"`
}
type metadata struct {
	// Type of the component.
	Type string `mapstructure:"type"`
	// Type of the parent component (applicable to subcomponents).
	Parent string `mapstructure:"parent"`
	// Status information for the component.
	Status *Status `mapstructure:"status"`
}

type githubData struct {
	folders           []string
	codeowners        []string
	allowlistFilePath string
	maxLength         int
	components        map[string]metadata
	dependabotData    *dependabotData
}

func loadMetadata(filePath string) (metadata, error) {
	cp, err := fileprovider.New().Retrieve(context.Background(), "file:"+filePath, nil)
	if err != nil {
		return metadata{}, err
	}

	conf, err := cp.AsConf()
	if err != nil {
		return metadata{}, err
	}

	md := metadata{}
	if err := conf.Unmarshal(&md); err != nil {
		return md, err
	}

	return md, nil
}

func run(folder string, allowlistFilePath string, generators []generator) error {

	components := map[string]metadata{}
	var foldersList []string
	dependabotData := &dependabotData{
		Version: 2,
		Updates: []dependabotUpdate{
			newDependabotUpdate("", 5),
		},
	}
	maxLength := 0
	allCodeowners := map[string]struct{}{}
	err := filepath.Walk(folder, func(path string, info fs.FileInfo, err error) error {
		if info.Name() == "metadata.yaml" {
			m, err := loadMetadata(path)
			if err != nil {
				return err
			}
			if m.Status == nil {
				return nil
			}
			currentFolder := filepath.Dir(path)
			key := currentFolder
			components[key] = m
			foldersList = append(foldersList, key)
			_, err = os.Stat(filepath.Join(currentFolder, "go.mod"))
			if err == nil { // go.mod file exists.
				dependabotData.Updates = append(dependabotData.Updates, newDependabotUpdate(currentFolder, makePriority(m.Status)))
			}
			for stability := range m.Status.Stability {
				if stability == unmaintainedStatus {
					// do not account for unmaintained status to change the max length of the component line.
					return nil
				}
			}
			if m.Status == nil || m.Status.Codeowners == nil {
				return fmt.Errorf("component %q has no status or codeowners section", key)
			}
			for _, id := range m.Status.Codeowners.Active {
				allCodeowners[id] = struct{}{}
			}
			if len(key) > maxLength {
				maxLength = len(key)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	sort.Strings(foldersList)
	codeownersList := make([]string, 0, len(allCodeowners))
	for c := range allCodeowners {
		codeownersList = append(codeownersList, c)
	}
	sort.Strings(codeownersList)

	data := &githubData{
		folders:           foldersList,
		codeowners:        codeownersList,
		allowlistFilePath: allowlistFilePath,
		maxLength:         maxLength,
		components:        components,
		dependabotData:    dependabotData,
	}

	for _, g := range generators {
		if err = g.generate(data); err != nil {
			return err
		}
	}
	return nil
}
