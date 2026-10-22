package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// Table is what `kubectl get` prints for a resource type: the API server's
// own columns (so custom resources look right too), plus each row's object
// metadata.
type Table struct {
	Type    ResourceType `json:"type"`
	Columns []Column     `json:"columns"`
	Rows    []TableRow   `json:"rows"`
}

type Column struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Format      string `json:"format,omitempty"`
	Description string `json:"description,omitempty"`
	Priority    int32  `json:"priority"` // 0 = shown by kubectl get, >0 = only with -o wide
}

type TableRow struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	UID       string            `json:"uid"`
	Created   string            `json:"created"`
	Labels    map[string]string `json:"labels,omitempty"`
	Owner     string            `json:"owner,omitempty"` // Kind/name of the controller
	Deleting  bool              `json:"deleting,omitempty"`
	Cells     []any             `json:"cells"`
}

// rawGet runs a GET on any API path.
func (c *Conn) raw() rest.Interface { return c.CS.CoreV1().RESTClient() }

// TableOptions narrow a listing.
type TableOptions struct {
	Namespace     string // "" = all namespaces
	LabelSelector string
	FieldSelector string
	Limit         int64
}

// List returns the table of a resource type.
func (c *Conn) List(ctx context.Context, rt ResourceType, o TableOptions) (*Table, error) {
	req := c.raw().Get().AbsPath(rt.Path(o.Namespace)).
		SetHeader("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io,application/json").
		Param("includeObject", "Metadata")
	if o.LabelSelector != "" {
		req = req.Param("labelSelector", o.LabelSelector)
	}
	if o.FieldSelector != "" {
		req = req.Param("fieldSelector", o.FieldSelector)
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 5000
	}
	req = req.Param("limit", strconv.FormatInt(limit, 10))
	b, err := req.Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	var t metav1.Table
	if err := json.Unmarshal(b, &t); err != nil || t.Kind != "Table" {
		return nil, fmt.Errorf("the API server did not return a table for %s", rt.ID)
	}
	out := &Table{Type: rt, Columns: []Column{}, Rows: []TableRow{}}
	for _, col := range t.ColumnDefinitions {
		out.Columns = append(out.Columns, Column{Name: col.Name, Type: col.Type, Format: col.Format, Description: col.Description, Priority: col.Priority})
	}
	for _, r := range t.Rows {
		row := TableRow{Cells: r.Cells}
		var m metav1.PartialObjectMetadata
		if len(r.Object.Raw) > 0 && json.Unmarshal(r.Object.Raw, &m) == nil {
			row.Name, row.Namespace, row.UID, row.Labels = m.Name, m.Namespace, string(m.UID), m.Labels
			row.Created = m.CreationTimestamp.UTC().Format(time.RFC3339)
			row.Deleting = m.DeletionTimestamp != nil
			for _, ref := range m.OwnerReferences {
				if ref.Controller != nil && *ref.Controller {
					row.Owner = ref.Kind + "/" + ref.Name
				}
			}
		}
		if row.Name == "" && len(r.Cells) > 0 {
			row.Name = fmt.Sprint(r.Cells[0])
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// Text renders a table like kubectl get (for the agent).
func (t *Table) Text(wide bool, max int) string {
	var cols []int
	for i, c := range t.Columns {
		if wide || c.Priority == 0 {
			cols = append(cols, i)
		}
	}
	var b strings.Builder
	header := []string{}
	if t.Type.Namespaced {
		header = append(header, "NAMESPACE")
	}
	for _, i := range cols {
		header = append(header, strings.ToUpper(t.Columns[i].Name))
	}
	b.WriteString(strings.Join(header, "\t") + "\n")
	for n, r := range t.Rows {
		if max > 0 && n >= max {
			fmt.Fprintf(&b, "… %d more\n", len(t.Rows)-max)
			break
		}
		cells := []string{}
		if t.Type.Namespaced {
			cells = append(cells, r.Namespace)
		}
		for _, i := range cols {
			v := ""
			if i < len(r.Cells) {
				v = fmt.Sprint(r.Cells[i])
			}
			cells = append(cells, v)
		}
		b.WriteString(strings.Join(cells, "\t") + "\n")
	}
	if len(t.Rows) == 0 {
		b.WriteString("(none)\n")
	}
	return b.String()
}
