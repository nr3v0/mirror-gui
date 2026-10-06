module github.com/nr3v0/mirror-gui

go 1.25.0

// node_modules contains a Go package (flatted) that is not part of this module.
ignore ./node_modules

require go.yaml.in/yaml/v3 v3.0.4
