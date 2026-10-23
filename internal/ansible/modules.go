package ansible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/sysproc"
)

// Module documentation for the editor, from the user's own ansible-doc:
// the modules of ansible-core and of every collection installed for the
// project, with their options.

// ModuleInfo is a module in the list.
type ModuleInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ModuleOption is one option (argument) of a module.
type ModuleOption struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`     // str, int, bool, list, dict, path, raw…
	Elements    string   `json:"elements,omitempty"` // element type of a list
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Choices     []any    `json:"choices,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description,omitempty"`
}

// ModuleDoc is a module's documentation.
type ModuleDoc struct {
	Name        string         `json:"name"`
	Short       string         `json:"short"`
	Description string         `json:"description,omitempty"`
	Options     []ModuleOption `json:"options"`
	FreeForm    bool           `json:"freeForm,omitempty"` // takes a free-form command (command, shell, raw…)
	Examples    string         `json:"examples,omitempty"`
}

type docCache struct {
	mu      sync.Mutex
	modules map[string][]ModuleInfo // per bin folder + project root
	docs    map[string]ModuleDoc
}

var modCache = &docCache{modules: map[string][]ModuleInfo{}, docs: map[string]ModuleDoc{}}

// docJSON runs ansible-doc and returns its standard output (warnings on
// stderr are ignored unless it fails).
func docJSON(ctx context.Context, p *Project, args ...string) ([]byte, error) {
	return runJSON(ctx, p, "ansible-doc", args...)
}

func runJSON(ctx context.Context, p *Project, prog string, args ...string) ([]byte, error) {
	path, err := lookPath(p, prog)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := sysproc.CommandContext(ctx, path, args...)
	cmd.Dir = p.Root
	cmd.Env = append(baseEnv(p), "ANSIBLE_FORCE_COLOR=0", "NO_COLOR=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %s", prog, lastLines(StripANSI(errb.String()+err.Error()), 4))
	}
	return out.Bytes(), nil
}

// Modules lists the modules available to a project (cached; refresh reads
// them again, e.g. after installing collections).
func (m *Manager) Modules(ctx context.Context, p *Project, refresh bool) ([]ModuleInfo, error) {
	key := binDir(p) + "|" + p.Root
	modCache.mu.Lock()
	if list, ok := modCache.modules[key]; ok && !refresh {
		modCache.mu.Unlock()
		return list, nil
	}
	modCache.mu.Unlock()
	raw, err := listModules(ctx, p, "")
	if err != nil {
		// One collection with broken documentation makes ansible-doc fail
		// the whole list; list the collections one by one instead.
		if raw, err = listByCollection(ctx, p); err != nil {
			return nil, err
		}
	}
	list := make([]ModuleInfo, 0, len(raw))
	for name, d := range raw {
		list = append(list, ModuleInfo{Name: name, Description: d})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	modCache.mu.Lock()
	modCache.modules[key] = list
	modCache.mu.Unlock()
	return list, nil
}

// listModules is ansible-doc's module list: name → short description
// (of one collection when given).
func listModules(ctx context.Context, p *Project, collection string) (map[string]string, error) {
	args := []string{"--list", "--json"}
	if collection != "" {
		args = append(args, collection)
	}
	b, err := docJSON(ctx, p, args...)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, errors.New("ansible-doc: unexpected output")
	}
	out := make(map[string]string, len(raw))
	for name, d := range raw {
		out[name] = text(d)
	}
	return out, nil
}

// listByCollection lists ansible.builtin and every installed collection
// separately, leaving out the collections ansible-doc cannot list.
func listByCollection(ctx context.Context, p *Project) (map[string]string, error) {
	names := []string{"ansible.builtin"}
	if b, err := runJSON(ctx, p, "ansible-galaxy", "collection", "list", "--format", "json"); err == nil {
		var paths map[string]map[string]any // collection path → name → details
		if json.Unmarshal(b, &paths) == nil {
			seen := map[string]bool{}
			for _, cs := range paths {
				for name := range cs {
					if !seen[name] {
						seen[name] = true
						names = append(names, name)
					}
				}
			}
		}
	}
	var (
		mu    sync.Mutex
		out   = map[string]string{}
		first error
		wg    sync.WaitGroup
		slots = make(chan struct{}, 8)
	)
	for _, c := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			list, err := listModules(ctx, p, c)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if c == "ansible.builtin" {
					first = err
				}
				return
			}
			for k, v := range list {
				out[k] = v
			}
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	return out, nil
}

// text joins ansible-doc's string-or-list descriptions.
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, p := range x {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// Doc returns a module's documentation (cached).
func (m *Manager) Doc(ctx context.Context, p *Project, name string) (ModuleDoc, error) {
	if !moduleRe.MatchString(name) {
		return ModuleDoc{}, fmt.Errorf("invalid module name %q", name)
	}
	key := binDir(p) + "|" + p.Root + "|" + name
	modCache.mu.Lock()
	if d, ok := modCache.docs[key]; ok {
		modCache.mu.Unlock()
		return d, nil
	}
	modCache.mu.Unlock()
	b, err := docJSON(ctx, p, "--json", "--", name)
	if err != nil {
		return ModuleDoc{}, err
	}
	var raw map[string]struct {
		Doc struct {
			Short       any                       `json:"short_description"`
			Description any                       `json:"description"`
			Options     map[string]map[string]any `json:"options"`
		} `json:"doc"`
		Examples any `json:"examples"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) == 0 {
		return ModuleDoc{}, fmt.Errorf("no documentation for %s", name)
	}
	var d ModuleDoc
	for full, r := range raw {
		d = ModuleDoc{Name: full, Short: text(r.Doc.Short), Description: text(r.Doc.Description), Examples: text(r.Examples), Options: []ModuleOption{}}
		for oname, o := range r.Doc.Options {
			if oname == "free_form" {
				d.FreeForm = true
				continue
			}
			opt := ModuleOption{Name: oname, Description: text(o["description"])}
			opt.Type, _ = o["type"].(string)
			opt.Elements, _ = o["elements"].(string)
			opt.Required, _ = o["required"].(bool)
			opt.Default = o["default"]
			if c, ok := o["choices"].([]any); ok {
				opt.Choices = c
			} else if c, ok := o["choices"].(map[string]any); ok { // choices with descriptions
				for k := range c {
					opt.Choices = append(opt.Choices, k)
				}
			}
			if a, ok := o["aliases"].([]any); ok {
				for _, x := range a {
					if s, ok := x.(string); ok {
						opt.Aliases = append(opt.Aliases, s)
					}
				}
			}
			d.Options = append(d.Options, opt)
		}
		sort.Slice(d.Options, func(i, j int) bool {
			if d.Options[i].Required != d.Options[j].Required {
				return d.Options[i].Required
			}
			return d.Options[i].Name < d.Options[j].Name
		})
	}
	modCache.mu.Lock()
	modCache.docs[key] = d
	modCache.mu.Unlock()
	return d, nil
}
