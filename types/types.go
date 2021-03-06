package types

import (
	"errors"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"

	"github.com/docker/app/internal"
	"github.com/docker/app/types/metadata"
	"github.com/docker/app/types/settings"
)

// SingleFileSeparator is the separator used in single-file app
const SingleFileSeparator = "\n---\n"

// App represents an app
type App struct {
	Name    string
	Path    string
	Cleanup func()

	composesContent [][]byte
	settingsContent [][]byte
	settings        settings.Settings
	metadataContent []byte
	metadata        metadata.AppMetadata
}

// Composes returns compose files content
func (a *App) Composes() [][]byte {
	return a.composesContent
}

// SettingsRaw returns setting files content
func (a *App) SettingsRaw() [][]byte {
	return a.settingsContent
}

// Settings returns map of settings
func (a *App) Settings() settings.Settings {
	return a.settings
}

// MetadataRaw returns metadata file content
func (a *App) MetadataRaw() []byte {
	return a.metadataContent
}

// Metadata returns the metadata struct
func (a *App) Metadata() metadata.AppMetadata {
	return a.metadata
}

// Extract writes the app in the specified folder
func (a *App) Extract(path string) error {
	if err := ioutil.WriteFile(filepath.Join(path, internal.MetadataFileName), a.MetadataRaw(), 0644); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(path, internal.ComposeFileName), a.Composes()[0], 0644); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(path, internal.SettingsFileName), a.SettingsRaw()[0], 0644); err != nil {
		return err
	}
	return nil
}

func noop() {}

// NewApp creates a new docker app with the specified path and struct modifiers
func NewApp(path string, ops ...func(*App) error) (*App, error) {
	app := &App{
		Name:    path,
		Path:    path,
		Cleanup: noop,

		composesContent: [][]byte{},
		settingsContent: [][]byte{},
		metadataContent: []byte{},
	}

	for _, op := range ops {
		if err := op(app); err != nil {
			return nil, err
		}
	}

	return app, nil
}

// NewAppFromDefaultFiles creates a new docker app using the default files in the specified path.
// If one of those file doesn't exists, it will error out.
func NewAppFromDefaultFiles(path string, ops ...func(*App) error) (*App, error) {
	appOps := append([]func(*App) error{
		MetadataFile(filepath.Join(path, internal.MetadataFileName)),
		WithComposeFiles(filepath.Join(path, internal.ComposeFileName)),
		WithSettingsFiles(filepath.Join(path, internal.SettingsFileName)),
	}, ops...)
	return NewApp(path, appOps...)
}

// WithName sets the application name
func WithName(name string) func(*App) error {
	return func(app *App) error {
		app.Name = name
		return nil
	}
}

// WithPath sets the original path of the app
func WithPath(path string) func(*App) error {
	return func(app *App) error {
		app.Path = path
		return nil
	}
}

// WithCleanup sets the cleanup function of the app
func WithCleanup(f func()) func(*App) error {
	return func(app *App) error {
		app.Cleanup = f
		return nil
	}
}

// WithSettingsFiles adds the specified settings files to the app
func WithSettingsFiles(files ...string) func(*App) error {
	return settingsLoader(func() ([][]byte, error) { return readFiles(files...) })
}

// WithSettings adds the specified settings readers to the app
func WithSettings(readers ...io.Reader) func(*App) error {
	return settingsLoader(func() ([][]byte, error) { return readReaders(readers...) })
}

func settingsLoader(f func() ([][]byte, error)) func(*App) error {
	return func(app *App) error {
		settingsContent, err := f()
		if err != nil {
			return err
		}
		settingsContents := append(app.settingsContent, settingsContent...)
		loaded, err := settings.LoadMultiple(settingsContents)
		if err != nil {
			return err
		}
		app.settings = loaded
		app.settingsContent = settingsContents
		return nil
	}
}

// MetadataFile adds the specified metadata file to the app
func MetadataFile(file string) func(*App) error {
	return metadataLoader(func() ([]byte, error) { return ioutil.ReadFile(file) })
}

// Metadata adds the specified metadata reader to the app
func Metadata(r io.Reader) func(*App) error {
	return metadataLoader(func() ([]byte, error) { return ioutil.ReadAll(r) })
}

func metadataLoader(f func() ([]byte, error)) func(app *App) error {
	return func(app *App) error {
		d, err := f()
		if err != nil {
			return err
		}
		loaded, err := metadata.Load(d)
		if err != nil {
			return err
		}
		app.metadata = loaded
		app.metadataContent = d
		return nil
	}
}

// WithComposeFiles adds the specified compose files to the app
func WithComposeFiles(files ...string) func(*App) error {
	return composeLoader(func() ([][]byte, error) { return readFiles(files...) })
}

// WithComposes adds the specified compose readers to the app
func WithComposes(readers ...io.Reader) func(*App) error {
	return composeLoader(func() ([][]byte, error) { return readReaders(readers...) })
}

func composeLoader(f func() ([][]byte, error)) func(app *App) error {
	return func(app *App) error {
		composesContent, err := f()
		if err != nil {
			return err
		}
		app.composesContent = append(app.composesContent, composesContent...)
		return nil
	}
}

func readReaders(readers ...io.Reader) ([][]byte, error) {
	content := make([][]byte, len(readers))
	var errs []string
	for i, r := range readers {
		d, err := ioutil.ReadAll(r)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		content[i] = d
	}
	return content, newErrGroup(errs)
}

func readFiles(files ...string) ([][]byte, error) {
	content := make([][]byte, len(files))
	var errs []string
	for i, file := range files {
		d, err := ioutil.ReadFile(file)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		content[i] = d
	}
	return content, newErrGroup(errs)
}

func newErrGroup(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "\n"))
}
