module github.com/nr3v0/mirror-gui

go 1.26.0

// node_modules contains a Go package (flatted) that is not part of this module.
ignore ./node_modules

require go.yaml.in/yaml/v3 v3.0.5

require (
	github.com/docker/cli v29.9.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.12 // indirect
	github.com/google/go-containerregistry v0.22.1
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)
