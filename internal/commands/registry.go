package commands

import (
	"sort"

	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

// GroupFactory builds a command group's root cobra.Command for one app.
type GroupFactory func(app *App, spec *appSpec) *cobra.Command

// groupReg is a self-registered group: its factory plus the apps it exists on.
type groupReg struct {
	name    string
	apps    []config.App
	factory GroupFactory
}

// registry holds every self-registered group. Group files call Register or
// RegisterFor from their init(), so the root command never needs editing to add
// a group — this is what keeps parallel group work conflict-free.
var registry []groupReg

// Register adds a group that exists on both apps.
func Register(name string, f GroupFactory) {
	registry = append(registry, groupReg{name: name, apps: []config.App{config.Radarr, config.Sonarr}, factory: f})
}

// RegisterFor adds a group that exists on one app only.
func RegisterFor(name string, app config.App, f GroupFactory) {
	registry = append(registry, groupReg{name: name, apps: []config.App{app}, factory: f})
}

// groupsFor returns the registered groups that exist on app, in registration
// order made deterministic by name.
func groupsFor(app config.App) []groupReg {
	out := []groupReg{}
	for _, g := range registry {
		for _, a := range g.apps {
			if a == app {
				out = append(out, g)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// addAll attaches every group registered for the sub-root's app, then hardens
// every parent (group) command so that:
//   - `<group> --llm` emits JSON help (the parent becomes Runnable, so the root
//     PersistentPreRunE's --llm short-circuit fires instead of cobra printing
//     human help and exiting 0), and
//   - `<group>` with no/unknown subcommand returns a JSON error envelope at
//     exit 1 instead of printing help at exit 0.
//
// Leaf commands (those with their own RunE) are left untouched.
func addAll(sub *cobra.Command, app *App, spec *appSpec) {
	for _, g := range groupsFor(spec.Name) {
		c := g.factory(app, spec)
		sub.AddCommand(c)
		hardenGroup(c)
	}
}

// hardenGroup recursively converts parent commands (subcommands present, no
// RunE) into erroring groups with cobra.NoArgs and a RunE that reports a
// missing subcommand. Leaves are skipped.
func hardenGroup(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		hardenGroup(c)
	}
	if !cmd.HasSubCommands() || cmd.RunE != nil || cmd.Run != nil {
		return
	}
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return output.UsageErrorHint(
			"missing subcommand for `"+c.CommandPath()+"`",
			"run `"+c.CommandPath()+" --help` to list subcommands, or `--llm` for JSON help")
	}
}
