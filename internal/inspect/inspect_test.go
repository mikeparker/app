package inspect

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/docker/app/internal"
	"github.com/docker/app/types"
	"gotest.tools/assert"
	"gotest.tools/fs"
	"gotest.tools/golden"
)

const (
	composeYAML = `version: "3.1"`
)

func TestInspect(t *testing.T) {
	dir := fs.NewDir(t, "inspect",
		fs.WithDir("no-maintainers",
			fs.WithFile(internal.ComposeFileName, composeYAML),
			fs.WithFile(internal.MetadataFileName, `
version: 0.1.0
name: foo`),
			fs.WithFile(internal.SettingsFileName, ``),
		),
		fs.WithDir("no-description",
			fs.WithFile(internal.ComposeFileName, composeYAML),
			fs.WithFile(internal.MetadataFileName, `
version: 0.1.0
name: foo
maintainers:
  - name: foo
    email: "foo@bar.com"`),
			fs.WithFile(internal.SettingsFileName, ""),
		),
		fs.WithDir("no-settings",
			fs.WithFile(internal.ComposeFileName, composeYAML),
			fs.WithFile(internal.MetadataFileName, `
version: 0.1.0
name: foo
maintainers:
  - name: foo
    email: "foo@bar.com"
description: "this is sparta !"`),
			fs.WithFile(internal.SettingsFileName, ""),
		),
		fs.WithDir("overridden",
			fs.WithFile(internal.ComposeFileName, `
version: "3.1"

services:
  web:
    image: nginx
    ports:
      - ${web.port}:80
`),
			fs.WithFile(internal.MetadataFileName, `
version: 0.1.0
name: foo
`),
			fs.WithFile(internal.SettingsFileName, ""),
		),
		fs.WithDir("full",
			fs.WithFile(internal.ComposeFileName, `
version: "3.1"

services:
  web:
    image: nginx:latest
    ports:
      - 8080-8100:12300-12320
    deploy:
      replicas: 2
networks:
  my-network:
volumes:
  my-volume:
secrets:
  my-secret:
    file: ./my_secret.txt
`),
			fs.WithFile(internal.MetadataFileName, `
version: 0.1.0
name: foo
maintainers:
  - name: foo
    email: "foo@bar.com"
description: "this is sparta !"`),
			fs.WithFile(internal.SettingsFileName, `
port: 8080
text: hello`),
		),
	)
	defer dir.Remove()

	for _, testcase := range []struct {
		name string
		args map[string]string
	}{
		{name: "no-maintainers"},
		{name: "no-description"},
		{name: "no-settings"},
		{name: "overridden", args: map[string]string{"web.port": "80"}},
		{name: "full"},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			outBuffer := new(bytes.Buffer)
			app, err := types.NewAppFromDefaultFiles(dir.Join(testcase.name))
			assert.NilError(t, err)
			err = Inspect(outBuffer, app, testcase.args)
			assert.NilError(t, err)
			assert.Assert(t, golden.String(outBuffer.String(), fmt.Sprintf("inspect-%s.golden", testcase.name)))
		})
	}
}
