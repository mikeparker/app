package renderer

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docker/app/packager"
	"github.com/docker/app/templateconversion"
	"github.com/docker/app/templateloader"
	"github.com/docker/app/templatev1beta2"
	"github.com/docker/app/types"
	"github.com/docker/app/utils"
	conversion "github.com/docker/cli/cli/command/stack/kubernetes"
	"github.com/docker/cli/cli/compose/loader"
	"github.com/docker/cli/kubernetes/compose/v1beta2"
	"github.com/pkg/errors"
	yaml "gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

/* Helm rendering with template preservation.

We modify compose.Type (in templatetypes) by replacing all bool by BoolOrTemplate,
all *uint64 with UInt64OrTemplate, etc.  so that we can store both a value or
a templated string.
We modify compose.Loader (in templateloader) to provide a new LoadTemplate that
skips schema validation and variable interpolation. MapStructure hooks are
provided for our *OrTemplate structs.
We modify v1beta2 Stack and associated structures (in templatev1beta2) in sync
with the changes in compose.Type, with the addition that all *OrTemplate structs
are yaml-serialized with a name prefied by 'template_'.
This package then invokes LoadTemplate, then templatev1beta2.convert, and
post-process the serialized yaml to replace all 'template_'-prefixed keys
with the appropriate content (value or template)
*/

type helmMaintainer struct {
	Name string
}

type helmMeta struct {
	Name        string
	Version     string
	Description string
	Keywords    []string
	Maintainers []helmMaintainer
}

func toHelmMeta(meta *types.AppMetadata) (*helmMeta, error) {
	res := &helmMeta{
		Name:        meta.Name,
		Version:     meta.Version,
		Description: meta.Description,
	}
	for _, m := range meta.Maintainers {
		res.Maintainers = append(res.Maintainers,
			helmMaintainer{Name: m.Name + " <" + m.Email + ">"},
		)
	}
	return res, nil
}

func mergeValues(target map[interface{}]interface{}, source map[string]interface{}) {
	for k, v := range source {
		tv, ok := target[k]
		if !ok {
			target[k] = v
			continue
		}
		switch tvv := tv.(type) {
		case map[interface{}]interface{}:
			mergeValues(tvv, v.(map[string]interface{}))
		default:
			target[k] = v
		}
	}
}

// remove from settings all stuff that is not in variables
func filterVariables(settings map[string]interface{}, variables []string, prefix string) {
	for k, v := range settings {
		switch vv := v.(type) {
		case map[string]interface{}:
			filterVariables(vv, variables, prefix+k+".")
			if len(vv) == 0 {
				delete(settings, k)
			}
		default:
			if !contains(variables, prefix+k) {
				delete(settings, k)
			}
		}
	}
}

// toGoTemplate converts $foo and ${foo} into {{.foo}}
func toGoTemplate(template string) (string, error) {
	re := regexp.MustCompile(`(^|[^$])\${?([a-zA-Z0-9_.]+)}?`)
	template = re.ReplaceAllString(template, "$1{{.Values.$2}}")
	template = strings.Replace(template, "$$", "$", -1)
	return template, nil
}

func convertTemplatesList(list []interface{}) error {
	for i, v := range list {
		switch vv := v.(type) {
		case string:
			vv, err := toGoTemplate(vv)
			if err != nil {
				return err
			}
			list[i] = vv
		case map[interface{}]interface{}:
			err := convertTemplates(vv)
			if err != nil {
				return err
			}
		case []interface{}:
			convertTemplatesList(vv)
		}
	}
	return nil
}

// convertTemplates replaces $foo with {{ .foo }}, and resolves template_ keys
func convertTemplates(dict map[interface{}]interface{}) error {
	for k, v := range dict {
		kk := k.(string)
		if strings.HasPrefix(kk, "template_") {
			dk := strings.TrimPrefix(kk, "template_")
			vd, ok := v.(map[interface{}]interface{})
			if !ok {
				return fmt.Errorf("Expected a map, got %T", v)
			}
			template, ok := vd["valuetemplate"]
			if ok {
				var err error
				template, err = toGoTemplate(template.(string))
				if err != nil {
					return err
				}
			} else {
				template = vd["value"]
			}
			delete(dict, k)
			dict[dk] = template
		} else {
			switch vv := v.(type) {
			case string:
				vv, err := toGoTemplate(vv)
				if err != nil {
					return err
				}
				dict[k] = vv
			case map[interface{}]interface{}:
				err := convertTemplates(vv)
				if err != nil {
					return err
				}
			case []interface{}:
				err := convertTemplatesList(vv)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func makeChart(appname, targetDir string) error {
	metaFile := filepath.Join(appname, "metadata.yml")
	metaContent, err := ioutil.ReadFile(metaFile)
	if err != nil {
		return err
	}
	var meta types.AppMetadata
	err = yaml.Unmarshal(metaContent, &meta)
	if err != nil {
		return err
	}
	hmeta, err := toHelmMeta(&meta)
	if err != nil {
		return err
	}
	chart := make(map[interface{}]interface{})
	prevChartRaw, err := ioutil.ReadFile(filepath.Join(targetDir, "Chart.yaml"))
	if err == nil {
		err = yaml.Unmarshal(prevChartRaw, chart)
		if err != nil {
			return err
		}
	}
	chart["name"] = hmeta.Name
	chart["version"] = hmeta.Version
	chart["description"] = hmeta.Description
	chart["keywords"] = hmeta.Keywords
	chart["maintainers"] = hmeta.Maintainers
	hmetadata, err := yaml.Marshal(chart)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(targetDir, "Chart.yaml"), hmetadata, 0644)
}

func helmRender(appname string, targetDir string, composeFiles []string, settingsFile []string, env map[string]string) error {
	rendered, err := Render(appname, composeFiles, settingsFile, env)
	if err != nil {
		return err
	}
	stackSpec := conversion.FromComposeConfig(rendered)
	stack := v1beta2.Stack{
		TypeMeta: metav1.TypeMeta{
			Kind:       "stacks.compose.docker.com",
			APIVersion: "v1beta2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: utils.AppNameFromDir(appname),
		},
		Spec: stackSpec,
	}
	stackData, err := yaml.Marshal(stack)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(targetDir, "templates", "stack.yaml"), stackData, 0644)
}

//makeStack converts data into a helm template for a stack
func makeStack(appname string, targetDir string, data []byte) error {
	parsed, err := loader.ParseYAML(data)
	if err != nil {
		return errors.Wrap(err, "failed to parse template compose")
	}
	rendered, err := templateloader.LoadTemplate(parsed)
	if err != nil {
		return errors.Wrap(err, "failed to load template compose")
	}
	os.Mkdir(filepath.Join(targetDir, "templates"), 0755)
	stackSpec := templateconversion.FromComposeConfig(rendered)
	stack := templatev1beta2.Stack{
		TypeMeta: metav1.TypeMeta{
			Kind:       "stacks.compose.docker.com",
			APIVersion: "v1beta2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      utils.AppNameFromDir(appname),
			Namespace: "default", // FIXME
		},
		Spec: stackSpec,
	}
	stackData, err := yaml.Marshal(stack)
	if err != nil {
		return err
	}
	preStack := make(map[interface{}]interface{})
	err = yaml.Unmarshal(stackData, preStack)
	if err != nil {
		return err
	}
	err = convertTemplates(preStack)
	if err != nil {
		return err
	}
	stackData, err = yaml.Marshal(preStack)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(targetDir, "templates", "stack.yaml"), stackData, 0644)
}

// Helm renders an app as an Helm Chart
func Helm(appname string, composeFiles []string, settingsFile []string, env map[string]string, render bool) error {
	appname, cleanup, err := packager.Extract(appname)
	if err != nil {
		return err
	}
	defer cleanup()
	targetDir := utils.AppNameFromDir(appname) + ".chart"
	if err := os.Mkdir(targetDir, 0755); err != nil && !os.IsExist(err) {
		return err
	}
	err = makeChart(appname, targetDir)
	if err != nil {
		return err
	}
	if render {
		return helmRender(appname, targetDir, composeFiles, settingsFile, env)
	}
	data, err := ioutil.ReadFile(filepath.Join(appname, "docker-compose.yml"))
	if err != nil {
		return err
	}
	variables, err := packager.ExtractVariables(string(data))
	if err != nil {
		return errors.Wrap(err, "failed to parse docker-compose.yml, maybe because it is a template")
	}
	err = makeStack(appname, targetDir, data)
	if err != nil {
		return err
	}
	return makeValues(appname, targetDir, settingsFile, env, variables)
}

// makeValues updates helm values.yaml with used variables from settings and env
func makeValues(appname, targetDir string, settingsFile []string, env map[string]string, variables []string) error {
	// merge our variables into Values.yaml
	sf := []string{filepath.Join(appname, "settings.yml")}
	sf = append(sf, settingsFile...)
	settings, err := LoadSettings(sf)
	if err != nil {
		return err
	}
	metaFile := filepath.Join(appname, "metadata.yml")
	meta := make(map[interface{}]interface{})
	metaContent, err := ioutil.ReadFile(metaFile)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(metaContent, &meta)
	if err != nil {
		return err
	}
	metaPrefixed := make(map[interface{}]interface{})
	metaPrefixed["app"] = meta
	merge(settings, metaPrefixed)
	err = MergeSettings(settings, env)
	if err != nil {
		return err
	}

	filterVariables(settings, variables, "")
	// merge settings with existing values.yml
	values := make(map[interface{}]interface{})
	if valuesCur, err := ioutil.ReadFile(filepath.Join(targetDir, "values.yaml")); err == nil {
		err = yaml.Unmarshal(valuesCur, values)
		if err != nil {
			return errors.Wrap(err, "failed to parse existing values.yaml")
		}
	}
	mergeValues(values, settings)
	valuesRaw, err := yaml.Marshal(values)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(targetDir, "values.yaml"), valuesRaw, 0644)
}
