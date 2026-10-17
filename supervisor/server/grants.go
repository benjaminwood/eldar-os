package server

import (
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Grants are bypasses Ben creates (or approves a session's request for). A
// grant auto-approves the requests it covers; each target has its own scope and
// never covers another:
//
//	eldar    commands in the agent's environment: session × profile ×
//	         credentials ("*", or a set the request's names must be a subset
//	         of; [] = only none) × aws (AWS profiles covered; none unless said)
//	mac      commands on Ben's Mac: session × profile × credentials
//	browser  Arc on the Mac: session × arc (Arc profile directories)
//
// Grants never override a policy deny or lockdown (checked first). In memory
// only: a restart clears them, and lockdown revokes them all.
type Grants struct {
	mu      sync.Mutex
	grants  map[int]*api.Grant
	counter int
	n       *Notifier
}

// Grants are Ben's standing exceptions to per-command approval, narrow by
// construction and in memory only (docs/decisions/0013-approvals-and-grants.md).
func NewGrants(n *Notifier) *Grants { return &Grants{grants: map[int]*api.Grant{}, n: n} }

func normCreds(c api.Creds) api.Creds {
	if c.Any {
		return api.AnyCreds
	}
	names := slices.Clone(c.Names)
	sort.Strings(names)
	return api.Creds{Names: slices.Compact(names)}
}

// Add creates a grant from validated input.
func (g *Grants) Add(in api.GrantInput) api.Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	target := in.Target
	if target == "" {
		target = "eldar" // callers validate targets (eldar, browser or a configured host)
	}
	gr := &api.Grant{
		Target:    target,
		Session:   in.Session,
		Profile:   in.Profile,
		CreatedAt: now.UnixMilli(),
		Note:      in.Note,
	}
	if gr.Session == "" {
		gr.Session = "*"
	}
	if gr.Profile == "" || target == "browser" {
		gr.Profile = "*"
	}
	if target != "browser" {
		gr.Credentials = normCreds(in.Credentials)
	}
	if target == "eldar" {
		gr.Aws = normCreds(in.Aws)
	}
	if target == "browser" {
		gr.Arc = normCreds(in.Arc)
		if len(in.ArcNames) > 0 {
			gr.ArcNames = in.ArcNames[:min(len(in.ArcNames), 50)]
		}
	}
	if in.Minutes != nil {
		exp := now.Add(time.Duration(max(1, *in.Minutes)) * time.Minute).UnixMilli()
		gr.ExpiresAt = &exp
	}
	g.counter++
	gr.ID = g.counter
	g.grants[gr.ID] = gr
	g.n.Changed()
	return *gr
}

func (g *Grants) Revoke(id int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.grants[id]; !ok {
		return false
	}
	delete(g.grants, id)
	g.n.Changed()
	return true
}

// Clear revokes every grant (lockdown); returns how many.
func (g *Grants) Clear() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.grants)
	clear(g.grants)
	if n > 0 {
		g.n.Changed()
	}
	return n
}

func (g *Grants) sweep() {
	now := time.Now().UnixMilli()
	for id, gr := range g.grants {
		if gr.ExpiresAt != nil && *gr.ExpiresAt <= now {
			delete(g.grants, id)
		}
	}
}

// GrantRequest is what a request asks for, as grants see it.
type GrantRequest struct {
	Target     string
	SessionID  string
	Verified   bool
	Profile    string   // browser: the Arc profile directory
	Creds      []string // nil = the whole profile
	AwsProfile string
}

// Match returns the first grant covering r (session-specific grants before
// global ones, so the audit names the narrowest), counting the use.
func (g *Grants) Match(r GrantRequest) *api.Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep()
	cands := make([]*api.Grant, 0, len(g.grants))
	for _, gr := range g.grants {
		cands = append(cands, gr)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		gi, gj := cands[i].Session == "*", cands[j].Session == "*"
		if gi != gj {
			return !gi
		}
		return cands[i].ID < cands[j].ID
	})
	for _, gr := range cands {
		if gr.Target != r.Target {
			continue
		}
		if gr.Session != "*" && !(r.Verified && r.SessionID == gr.Session) {
			continue
		}
		if r.Target == "browser" {
			if !gr.Arc.Any && !slices.Contains(gr.Arc.Names, r.Profile) {
				continue
			}
		} else {
			if gr.Profile != "*" && gr.Profile != r.Profile {
				continue
			}
			if !credsCovered(gr.Credentials, r.Creds) {
				continue
			}
			if r.AwsProfile != "" && !gr.Aws.Any && !slices.Contains(gr.Aws.Names, r.AwsProfile) {
				continue
			}
		}
		gr.Uses++
		g.n.Changed()
		out := *gr
		return &out
	}
	return nil
}

// credsCovered: "*" covers anything, including the whole profile (nil); a set
// covers requests whose names are all in it.
func credsCovered(grant api.Creds, req []string) bool {
	if grant.Any {
		return true
	}
	if req == nil {
		return false
	}
	for _, c := range req {
		if !slices.Contains(grant.Names, c) {
			return false
		}
	}
	return true
}

// List returns live grants by ID.
func (g *Grants) List() []api.Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep()
	out := []api.Grant{}
	for _, gr := range g.grants {
		out = append(out, *gr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
