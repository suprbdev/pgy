package group

import (
    "fmt"
    "path"
    "path/filepath"
    "sort"
    "strings"

    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/schema"
)

const (
    ModeFile = "file"
    ModeDir  = "dir"
)

type Group struct {
    Name  string
    Files []string
}

type Step struct {
    Name string
    Plan *diff.PlanDiff
}

func Split(paths []string, mode string) ([]Group, error) {
    if mode != ModeFile && mode != ModeDir {
        return nil, fmt.Errorf("invalid split mode %q (expected %q or %q)", mode, ModeFile, ModeDir)
    }
    root := commonDir(paths)
    var groups []Group
    pos := map[string]int{}
    for _, p := range paths {
        rel, err := filepath.Rel(root, p)
        if err != nil { rel = p }
        rel = filepath.ToSlash(rel)
        name := strings.TrimSuffix(rel, path.Ext(rel))
        if mode == ModeDir {
            name = path.Dir(rel)
            if name == "." { name = dirName(root) }
        }
        if i, ok := pos[name]; ok {
            groups[i].Files = append(groups[i].Files, p)
            continue
        }
        pos[name] = len(groups)
        groups = append(groups, Group{Name: name, Files: []string{p}})
    }
    return groups, nil
}

func commonDir(paths []string) string {
    var common []string
    for i, p := range paths {
        parts := strings.Split(filepath.ToSlash(filepath.Dir(filepath.Clean(p))), "/")
        if i == 0 {
            common = parts
            continue
        }
        n := 0
        for n < len(common) && n < len(parts) && common[n] == parts[n] { n++ }
        common = common[:n]
    }
    switch {
    case len(common) == 0:
        return "."
    case len(common) == 1 && common[0] == "":
        return "/"
    }
    return filepath.FromSlash(strings.Join(common, "/"))
}

func dirName(dir string) string {
    if abs, err := filepath.Abs(dir); err == nil { dir = abs }
    if name := filepath.Base(dir); name != "." && name != string(filepath.Separator) { return name }
    return "schema"
}

func Plan(live *diff.Live, paths []string, groups []Group, unsafe bool) ([]Step, error) {
    full, err := schema.LoadAndMerge(paths)
    if err != nil { return nil, err }
    files := map[string]*schema.Database{}
    for _, p := range paths {
        d, err := schema.LoadAndMerge([]string{p})
        if err != nil { return nil, err }
        files[p] = d
    }
    ix := newIndex(full)
    ordered := order(groups, files, ix)

    steps := []Step{}
    included := map[string]bool{}
    var prev *schema.Database
    for i, g := range ordered {
        for _, f := range g.Files { included[f] = true }
        sel := make([]string, 0, len(paths))
        for _, p := range paths {
            if included[p] { sel = append(sel, p) }
        }
        cur, err := schema.LoadAndMerge(sel)
        if err != nil { return nil, err }
        cur.Extensions = full.Extensions
        cur.Roles = full.Roles
        (&availability{ix: ix, cur: cur, live: live}).deferUnready()
        if prev != nil {
            for k, vw := range cur.Views {
                if vw != nil && prev.Views[k] != nil { vw.Replace = false }
            }
        }

        p := diff.Plan(diff.Simulate(live, prev), cur, unsafe)
        if i < len(ordered)-1 { p.Drops = nil }
        if len(p.Creates)+len(p.Alters)+len(p.Drops) > 0 {
            steps = append(steps, Step{Name: g.Name, Plan: p})
        }
        prev = cur
    }
    return steps, nil
}

func order(groups []Group, files map[string]*schema.Database, ix *index) []Group {
    n := len(groups)
    hard := make([]map[int]bool, n)
    soft := make([]map[int]bool, n)
    for i := range groups {
        hard[i] = map[int]bool{}
        soft[i] = map[int]bool{}
    }
    home := homes(groups, files)
    for gi, g := range groups {
        own := map[ref]bool{}
        for _, f := range g.Files {
            for _, r := range declared(files[f]) { own[r] = true }
        }
        link := func(set []map[int]bool, refs []ref) {
            for _, r := range refs {
                if h, ok := home[r]; ok && h != gi && !own[r] { set[gi][h] = true }
            }
        }
        for _, f := range g.Files {
            d := files[f]
            for fq, t := range d.Tables {
                if h, ok := home[ref{kindTable, fq}]; ok && h != gi && t != nil { hard[gi][h] = true }
            }
            collectEdges(d, ix, func(r []ref) { link(hard, r) }, func(r []ref) { link(soft, r) })
        }
    }

    comps := components(n, hard)
    comp := make([]int, n)
    for ci, members := range comps {
        for _, m := range members { comp[m] = ci }
    }
    nc := len(comps)
    chard := make([]map[int]bool, nc)
    csoft := make([]map[int]bool, nc)
    for ci := range comps {
        chard[ci] = map[int]bool{}
        csoft[ci] = map[int]bool{}
    }
    for gi := 0; gi < n; gi++ {
        for h := range hard[gi] {
            if comp[h] != comp[gi] { chard[comp[gi]][comp[h]] = true }
        }
        for h := range soft[gi] {
            if comp[h] != comp[gi] { csoft[comp[gi]][comp[h]] = true }
        }
    }

    done := make([]bool, nc)
    satisfied := func(deps map[int]bool) bool {
        for d := range deps {
            if !done[d] { return false }
        }
        return true
    }
    out := make([]Group, 0, nc)
    for len(out) < nc {
        pick, fallback := -1, -1
        for ci := 0; ci < nc; ci++ {
            if done[ci] || !satisfied(chard[ci]) { continue }
            if fallback < 0 { fallback = ci }
            if satisfied(csoft[ci]) {
                pick = ci
                break
            }
        }
        if pick < 0 { pick = fallback }
        done[pick] = true
        out = append(out, mergeGroups(groups, comps[pick]))
    }
    return out
}

func homes(groups []Group, files map[string]*schema.Database) map[ref]int {
    home := map[ref]int{}
    owner := map[ref]bool{}
    for gi, g := range groups {
        for _, f := range g.Files {
            d := files[f]
            for _, r := range declared(d) {
                owns := r.kind == kindTable && ownsTable(d.Tables[r.key])
                if _, ok := home[r]; !ok || (owns && !owner[r]) {
                    home[r] = gi
                    owner[r] = owns
                }
            }
        }
    }
    return home
}

func ownsTable(t *schema.Table) bool {
    if len(t.PrimaryKey) > 0 || t.PartitionOf != "" || t.PartitionBy != nil { return true }
    for _, c := range t.Columns {
        if c != nil && c.PrimaryKey { return true }
    }
    return false
}

func collectEdges(d *schema.Database, ix *index, hard, soft func([]ref)) {
    if d == nil { return }
    all := allKinds
    for _, t := range d.Tables {
        if t == nil { continue }
        hard(ix.resolveDeps(t.DependsOn))
        if t.PartitionOf != "" { hard([]ref{{kindTable, t.PartitionOf}}) }
        for _, c := range t.Columns {
            if c == nil { continue }
            hard(ix.mentions(c.Type, typeKinds))
            soft(ix.mentions(c.Default, defaultKinds))
        }
        for _, fk := range t.ForeignKeys {
            if fk != nil { soft([]ref{{kindTable, qualifyPublic(fk.RefTable)}}) }
        }
        for _, x := range t.Indexes {
            if x != nil { soft(ix.mentions(strings.Join(x.Columns, " ")+" "+x.Where, exprKinds)) }
        }
        for _, ct := range t.Constraints {
            if ct != nil { soft(ix.mentions(ct.Expression, exprKinds)) }
        }
        for _, tr := range t.Triggers {
            if tr != nil { soft(ix.mentions(tr.Procedure+" "+tr.When, callKinds)) }
        }
        for _, p := range t.Policies {
            if p != nil { soft(ix.mentions(p.Using+" "+p.WithCheck, policyKinds)) }
        }
    }
    for _, f := range d.Functions {
        if f == nil { continue }
        hard(ix.resolveDeps(f.DependsOn))
        hard(ix.mentions(f.ArgsSig, typeKinds))
        hard(ix.mentions(f.Returns, returnKinds))
        bodyEdges(f.Language, hard, soft)(ix.mentions(f.Body, all))
    }
    for _, p := range d.Procedures {
        if p == nil { continue }
        hard(ix.resolveDeps(p.DependsOn))
        hard(ix.mentions(p.ArgsSig, typeKinds))
        bodyEdges(p.Language, hard, soft)(ix.mentions(p.Body, all))
    }
    for _, td := range d.Types {
        if td == nil { continue }
        hard(ix.resolveDeps(td.DependsOn))
        for _, at := range td.Attributes { hard(ix.mentions(at, typeKinds)) }
    }
    for _, dm := range d.Domains {
        if dm == nil { continue }
        hard(ix.resolveDeps(dm.DependsOn))
        hard(ix.mentions(dm.Type, typeKinds))
        hard(ix.mentions(dm.Check+" "+dm.Default, exprKinds))
    }
    for _, sq := range d.Sequences {
        if sq == nil { continue }
        hard(ix.resolveDeps(sq.DependsOn))
        if t := ownedByTable(sq.OwnedBy); t != "" { hard([]ref{{kindTable, t}}) }
    }
    for _, vw := range d.Views {
        if vw == nil { continue }
        hard(ix.resolveDeps(vw.DependsOn))
        hard(ix.mentions(vw.Query, all))
    }
}

func bodyEdges(language string, hard, soft func([]ref)) func([]ref) {
    if strings.EqualFold(strings.TrimSpace(language), "sql") { return hard }
    return soft
}

func components(n int, edges []map[int]bool) [][]int {
    idx := make([]int, n)
    low := make([]int, n)
    on := make([]bool, n)
    for i := range idx { idx[i] = -1 }
    var stack []int
    var out [][]int
    next := 0
    var visit func(v int)
    visit = func(v int) {
        idx[v], low[v] = next, next
        next++
        stack = append(stack, v)
        on[v] = true
        targets := make([]int, 0, len(edges[v]))
        for w := range edges[v] { targets = append(targets, w) }
        sort.Ints(targets)
        for _, w := range targets {
            if idx[w] < 0 {
                visit(w)
                low[v] = min(low[v], low[w])
            } else if on[w] {
                low[v] = min(low[v], idx[w])
            }
        }
        if low[v] == idx[v] {
            var members []int
            for {
                w := stack[len(stack)-1]
                stack = stack[:len(stack)-1]
                on[w] = false
                members = append(members, w)
                if w == v { break }
            }
            sort.Ints(members)
            out = append(out, members)
        }
    }
    for v := 0; v < n; v++ {
        if idx[v] < 0 { visit(v) }
    }
    sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
    return out
}

func mergeGroups(groups []Group, members []int) Group {
    if len(members) == 1 { return groups[members[0]] }
    names := make([]string, 0, len(members))
    var files []string
    for _, m := range members {
        names = append(names, groups[m].Name)
        files = append(files, groups[m].Files...)
    }
    return Group{Name: strings.Join(names, "+"), Files: files}
}
