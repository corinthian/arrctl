package commands

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// llmHelp is the machine-readable per-command help object emitted by --llm.
type llmHelp struct {
	Command      string     `json:"command"`
	App          string     `json:"app,omitempty"`
	Usage        string     `json:"usage"`
	Summary      string     `json:"summary"`
	Args         []string   `json:"args"`
	Flags        []llmFlag  `json:"flags"`
	Examples     []string   `json:"examples"`
	OutputSchema string     `json:"output_schema"`
	Subcommands  []llmChild `json:"subcommands,omitempty"`
}

type llmFlag struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage"`
	Scope   string `json:"scope"`
}

type llmChild struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// emitLLMHelp prints the JSON help object for cmd. Endpoint, examples and
// output_schema come from cmd.Annotations when a group sets them; everything
// else is derived from the cobra command.
func emitLLMHelp(cmd *cobra.Command, out *output.Writer) {
	h := llmHelp{
		Command: cmd.CommandPath(),
		Usage:   cmd.UseLine(),
		Summary: cmd.Short,
		Args:    []string{},
		Flags:   []llmFlag{},
	}
	if app, ok := targetApp(cmd); ok {
		h.App = string(app)
	}
	if len(cmd.ValidArgs) > 0 {
		h.Args = cmd.ValidArgs
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		scope := "inherited"
		if cmd.LocalFlags().Lookup(f.Name) != nil {
			scope = "local"
		}
		h.Flags = append(h.Flags, llmFlag{
			Name: f.Name, Type: f.Value.Type(), Default: f.DefValue, Usage: f.Usage, Scope: scope,
		})
	})
	if ex := cmd.Annotations["examples"]; ex != "" {
		h.Examples = strings.Split(ex, "\n")
	} else {
		h.Examples = synthExamples(cmd)
	}
	if os := cmd.Annotations["output_schema"]; os != "" {
		h.OutputSchema = os
	} else {
		h.OutputSchema = defaultOutputSchema(cmd)
	}
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		h.Subcommands = append(h.Subcommands, llmChild{Name: c.Name(), Summary: c.Short})
	}
	data, _ := json.MarshalIndent(h, "", "  ")
	_, _ = out.Out.Write(append(data, '\n'))
}

// synthExamples builds at least one usable invocation when a command supplies
// none via annotations, so the --llm `examples` key is never null.
func synthExamples(cmd *cobra.Command) []string {
	path := cmd.CommandPath()
	if cmd.HasSubCommands() {
		return []string{path + " --help", path + " --llm"}
	}
	ex := path
	for _, f := range []string{"id", "name", "term", "payload", "title", "start", "date"} {
		if fl := cmd.LocalFlags().Lookup(f); fl != nil {
			ex += " --" + f + " <" + f + ">"
		}
	}
	return []string{ex, ex + " --terse"}
}

func defaultOutputSchema(cmd *cobra.Command) string {
	if ep := cmd.Annotations["endpoint"]; ep != "" {
		return "JSON envelope: {ok:bool, data:object|array|null, meta:{app,endpoint,duration_ms," +
			"pagination?}} on success; {ok:false, error:{code,message,http_status?,hint?}, meta:{app,...}} " +
			"on failure. data is the app's raw response for " + ep + "."
	}
	return "JSON envelope: {ok:bool, data:object|array|null, meta:{app,endpoint,duration_ms," +
		"pagination?}} on success; {ok:false, error:{code,message,http_status?,hint?}} on failure."
}

// groupNode is one entry of the `arrctl commands` groups array: the group name,
// the apps it exists on, and its verbs.
type groupNode struct {
	Name  string     `json:"name"`
	Apps  []string   `json:"apps"`
	Verbs []verbNode `json:"verbs"`
}

type verbNode struct {
	Name    string     `json:"name"`
	Summary string     `json:"summary"`
	Paths   []string   `json:"paths"`
	Verbs   []verbNode `json:"verbs,omitempty"`
}

// commandTree is the `arrctl commands` payload.
type commandTree struct {
	Binary  string      `json:"binary"`
	Version string      `json:"version"`
	Apps    []string    `json:"apps"`
	Groups  []groupNode `json:"groups"`
	Root    []verbNode  `json:"root_commands"`
}

// buildCommandTree emits one entry per group name with the union of the apps it
// exists on, rather than walking the cobra tree (which would list every shared
// group twice, once under each sub-root, and carry no `apps` field).
func buildCommandTree(root *cobra.Command) commandTree {
	t := commandTree{Binary: "arrctl", Version: Version}
	for _, a := range config.Apps {
		t.Apps = append(t.Apps, string(a))
	}

	byName := map[string]*groupNode{}
	order := []string{}
	for _, a := range config.Apps {
		sub := findChild(root, string(a))
		if sub == nil {
			continue
		}
		for _, g := range sub.Commands() {
			if g.Hidden || g.Name() == "help" || g.Name() == "completion" {
				continue
			}
			n, ok := byName[g.Name()]
			if !ok {
				n = &groupNode{Name: g.Name(), Apps: []string{}, Verbs: verbsOf(g)}
				byName[g.Name()] = n
				order = append(order, g.Name())
			}
			n.Apps = append(n.Apps, string(a))
			n.Verbs = mergeVerbs(n.Verbs, verbsOf(g))
		}
	}
	sort.Strings(order)
	for _, name := range order {
		t.Groups = append(t.Groups, *byName[name])
	}

	for _, c := range root.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		if _, isApp := c.Annotations["app"]; isApp {
			continue
		}
		t.Root = append(t.Root, verbNode{Name: c.Name(), Summary: c.Short, Verbs: verbsOf(c)})
	}
	return t
}

func verbsOf(g *cobra.Command) []verbNode {
	out := []verbNode{}
	for _, c := range g.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		v := verbNode{Name: c.Name(), Summary: c.Short}
		if ep := c.Annotations["endpoint"]; ep != "" {
			v.Paths = []string{ep}
		}
		if c.HasSubCommands() {
			v.Verbs = verbsOf(c)
		}
		out = append(out, v)
	}
	return out
}

// mergeVerbs unions the endpoint list of a shared group across both apps, so a
// group whose path differs per app (history item) shows both.
func mergeVerbs(a, b []verbNode) []verbNode {
	idx := map[string]int{}
	for i, v := range a {
		idx[v.Name] = i
	}
	for _, v := range b {
		i, ok := idx[v.Name]
		if !ok {
			a = append(a, v)
			idx[v.Name] = len(a) - 1
			continue
		}
		a[i].Paths = union(a[i].Paths, v.Paths)
		a[i].Verbs = mergeVerbs(a[i].Verbs, v.Verbs)
	}
	return a
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// newVersionCmd implements `arrctl version`, the app-independent verb.
func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the arrctl version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.emitLocal(map[string]string{
				"binary":  "arrctl",
				"version": Version,
			}, "arrctl "+Version)
		},
	}
}

// newCommandsCmd implements `arrctl commands`: the whole tree as JSON, both
// apps, each group tagged with the apps it exists on.
func newCommandsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "Emit the full command tree as JSON, both apps",
		Annotations: map[string]string{
			"examples": "arrctl commands | jq '.data.groups[] | select(.name==\"queue\").apps'\n" +
				"arrctl commands | jq -r '.data.groups[].name'",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.emitLocal(buildCommandTree(cmd.Root()), "")
		},
	}
}
