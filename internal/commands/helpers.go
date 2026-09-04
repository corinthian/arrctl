package commands

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

func itoa(n int) string { return strconv.Itoa(n) }

// pagingQuery returns the root-level paging flags as query params. Only flags
// the caller changed are sent, so the instance's own defaults apply otherwise.
func (a *App) pagingQuery(cmd *cobra.Command) url.Values {
	q := url.Values{}
	root := cmd.Root().PersistentFlags()
	if root.Changed("page") {
		q.Set("page", itoa(a.Flags.Page))
	}
	if root.Changed("page-size") {
		q.Set("pageSize", itoa(a.Flags.PageSize))
	}
	if root.Changed("sort-key") {
		q.Set("sortKey", a.Flags.SortKey)
	}
	if root.Changed("sort-dir") {
		q.Set("sortDirection", a.Flags.SortDir)
	}
	return q
}

// merge copies src into dst.
func merge(dst, src url.Values) url.Values {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	return dst
}

// listOpts builds client options for a paged GET: root paging flags, --all, and
// the command's own bound query flags.
func (a *App) listOpts(cmd *cobra.Command, b *qbind) (client.Options, *output.CLIError) {
	q := a.pagingQuery(cmd)
	own, cerr := b.Values()
	if cerr != nil {
		return client.Options{}, cerr
	}
	return client.Options{Query: merge(q, own), All: a.Flags.All}, nil
}

// simpleGet builds a no-flag GET command.
func (a *App) simpleGet(use, short, path string) *cobra.Command {
	return &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": client.APIPrefix + path},
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.get(path, client.Options{})
			if err != nil {
				return err
			}
			return a.emit(res, "")
		},
	}
}

// queryGet builds a GET command whose only inputs are bound qflags.
func (a *App) queryGet(use, short, path string, flags []qflag) *cobra.Command {
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": client.APIPrefix + path},
	}
	b := bindQ(c, flags)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		res, err := a.get(path, client.Options{Query: q})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

// pagedGet builds a paged list GET: root paging flags plus the given qflags.
func (a *App) pagedGet(use, short, path string, flags []qflag) *cobra.Command {
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": client.APIPrefix + path},
	}
	b := bindQ(c, flags)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		opts, cerr := a.listOpts(cmd, b)
		if cerr != nil {
			return cerr
		}
		res, err := a.get(path, opts)
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

// idFlag registers a required --id on cmd and returns the pointer.
func idFlag(c *cobra.Command, usage string) *int {
	p := new(int)
	c.Flags().IntVar(p, "id", 0, usage)
	return p
}

// requireID fails when --id was not given a positive value.
func requireID(cmd *cobra.Command, v int) *output.CLIError {
	if !cmd.Flags().Changed("id") || v <= 0 {
		return output.UsageError("missing required --id (a positive integer)")
	}
	return nil
}

// getByID builds `<use> --id N`: GET <prefix>/{id}<suffix>.
func (a *App) getByID(use, short, prefix, suffix, usage string) *cobra.Command {
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": client.APIPrefix + prefix + "/{id}" + suffix},
	}
	id := idFlag(c, usage)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		res, err := a.get(prefix+"/"+itoa(*id)+suffix, client.Options{})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

// payloadCmd builds a mutating command taking --payload / --payload-file.
// gateWhat, when non-empty, names the action in a confirm gate.
func (a *App) payloadCmd(use, short, method, path, gateWhat string) *cobra.Command {
	var payload, payloadFile string
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": method + " " + client.APIPrefix + path},
		RunE: func(cmd *cobra.Command, args []string) error {
			if gateWhat != "" {
				if cerr := a.gate(gateWhat); cerr != nil {
					return cerr
				}
			}
			body, err := resolvePayload(payload, payloadFile)
			if err != nil {
				return err
			}
			res, derr := a.do(method, path, client.Options{Body: body})
			if derr != nil {
				return derr
			}
			return a.emit(res, "")
		},
	}
	addPayloadFlags(c, &payload, &payloadFile)
	return c
}

// payloadByIDCmd builds `<use> --id N --payload JSON`: <method> <prefix>/{id}.
func (a *App) payloadByIDCmd(use, short, method, prefix, gateWhat, idUsage string, extra []qflag) *cobra.Command {
	var payload, payloadFile string
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": method + " " + client.APIPrefix + prefix + "/{id}"},
	}
	id := idFlag(c, idUsage)
	b := bindQ(c, extra)
	addPayloadFlags(c, &payload, &payloadFile)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if gateWhat != "" {
			if cerr := a.gate(gateWhat); cerr != nil {
				return cerr
			}
		}
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		body, err := resolvePayload(payload, payloadFile)
		if err != nil {
			return err
		}
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		res, derr := a.do(method, prefix+"/"+itoa(*id), client.Options{Body: body, Query: q})
		if derr != nil {
			return derr
		}
		return a.emit(res, "")
	}
	return c
}

// deleteByIDCmd builds `<use> --id N`: DELETE <prefix>/{id}. Always gated.
func (a *App) deleteByIDCmd(use, short, prefix, gateWhat, idUsage string, extra []qflag) *cobra.Command {
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "DELETE " + client.APIPrefix + prefix + "/{id}"},
	}
	id := idFlag(c, idUsage)
	b := bindQ(c, extra)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := a.gate(gateWhat); cerr != nil {
			return cerr
		}
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		res, err := a.del(prefix+"/"+itoa(*id), client.Options{Query: q})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

func addPayloadFlags(c *cobra.Command, payload, payloadFile *string) {
	c.Flags().StringVar(payload, "payload", "", "request body as JSON")
	c.Flags().StringVar(payloadFile, "payload-file", "",
		"request body as JSON read from a file (\"-\" for stdin); mutually exclusive with --payload")
}

// crudGroup builds the list/get/add/update/delete quintet shared by rootfolder,
// qualityprofile and tag. Verbs the caller does not want are omitted by leaving
// the corresponding method string empty.
type crudGroup struct {
	Use      string
	Short    string
	Path     string
	ListOnly bool
	AddGate  string
	DelGate  string
}

func (a *App) newCRUD(g crudGroup) *cobra.Command {
	root := &cobra.Command{Use: g.Use, Short: g.Short}
	root.AddCommand(a.simpleGet("list", "List "+g.Use+" records", g.Path))
	root.AddCommand(a.getByID("get", "Get one "+g.Use+" record", g.Path, "", g.Use+" id"))
	if g.ListOnly {
		return root
	}
	root.AddCommand(a.payloadCmd("add", "Create a "+g.Use+" record", http.MethodPost, g.Path, g.AddGate))
	root.AddCommand(a.payloadByIDCmd("update", "Replace a "+g.Use+" record",
		http.MethodPut, g.Path, "", g.Use+" id", nil))
	root.AddCommand(a.deleteByIDCmd("delete", "Delete a "+g.Use+" record",
		g.Path, "deleting a "+g.Use+" record", g.Use+" id", nil))
	return root
}
