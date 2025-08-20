package snippet

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// Fetcher interface represents fetch several resources from some sources.
// Remote Fetcher    - Fastly Managed
// Terraform Fetcher - Terraform Planned Result
type Fetcher interface {
	// Caching methods
	LookupCache(bool) *Snippets
	WriteCache(*Snippets)

	// Resource fetching methods
	Backends() ([]*Backend, error)
	Directors() ([]*Director, error)
	Dictionaries() ([]*Dictionary, error)
	Acls() ([]*Acl, error)
	Conditions() ([]*Condition, error)
	Snippets() ([]*VCLSnippet, error)
	Headers() ([]*Header, error)
	ResponseObjects() ([]*ResponseObject, error)
	RequestSetting() (*RequestSetting, error)
	LoggingEndpoints() ([]string, error)
}

func Fetch(fetcher Fetcher) (*Snippets, error) {
	snippets := &Snippets{
		ScopedSnippets:   ScopedSnippets{},
		IncludeSnippets:  IncludeSnippets{},
		LoggingEndpoints: LoggingEndpoints{},
	}

	var eg errgroup.Group

	fmt.Print("Fetching snippets...")
	eg.Go(func() (err error) {
		snippets.Dictionaries, err = fetchEdgeDictionary(fetcher)
		return err
	})
	eg.Go(func() (err error) {
		snippets.Acls, err = fetchAccessControl(fetcher)
		return err
	})
	eg.Go(func() (err error) {
		snippets.Backends, err = fetchBackend(fetcher)
		return err
	})
	eg.Go(func() (err error) {
		snippets.Directors, err = fetchDirector(fetcher)
		return err
	})
	eg.Go(func() (err error) {
		snippets.ScopedSnippets, snippets.IncludeSnippets, err = fetchVCLSnippets(fetcher)
		return err
	})
	eg.Go(func() (err error) {
		snippets.Conditions, err = fetchConditions(fetcher)
		return err
	})
	eg.Go(func() (err error) {
		snippets.Headers, err = fetcher.Headers()
		return err
	})
	eg.Go(func() (err error) {
		snippets.ResponseObjects, err = fetcher.ResponseObjects()
		return err
	})
	eg.Go(func() (err error) {
		snippets.RequestSetting, err = fetcher.RequestSetting()
		return err
	})

	if err := eg.Wait(); err != nil {
		fmt.Println("Error!")
		return nil, errors.WithStack(err)
	}

	// Generate backend selection snippets for recv scope after all data is fetched
	backendSelectionSnippets, err := generateBackendSelectionSnippets(fetcher)
	if err != nil {
		fmt.Println("Error!")
		return nil, errors.WithStack(err)
	}

	if len(backendSelectionSnippets) > 0 {
		if _, ok := snippets.ScopedSnippets["recv"]; !ok {
			snippets.ScopedSnippets["recv"] = []Item{}
		}
		snippets.ScopedSnippets["recv"] = append(snippets.ScopedSnippets["recv"], backendSelectionSnippets...)
	}

	fmt.Println("Done.")
	return snippets, nil
}

func fetchEdgeDictionary(fetcher Fetcher) ([]Item, error) {
	dicts, err := fetcher.Dictionaries()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var snippets []Item
	for _, dict := range dicts {
		snip, err := renderDictionary(dict)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		snippets = append(snippets, *snip)
	}
	return snippets, nil
}

func fetchAccessControl(fetcher Fetcher) ([]Item, error) {
	acls, err := fetcher.Acls()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var snippets []Item
	for _, a := range acls {
		snip, err := renderAcl(a)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		snippets = append(snippets, *snip)
	}
	return snippets, nil
}

func fetchBackend(fetcher Fetcher) ([]Item, error) {
	var snippets []Item
	backends, err := fetcher.Backends()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if len(backends) == 0 {
		return snippets, nil
	}

	for _, b := range backends {
		snip, err := renderBackend(b)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		snippets = append(snippets, *snip)
	}

	// Generate director snippet only when at least one backend is declared
	if len(backends) > 0 {
		directors, err := renderBackendShields(backends)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		snippets = append(snippets, directors...)
	}

	return snippets, nil
}

func fetchDirector(fetcher Fetcher) ([]Item, error) {
	var snippets []Item
	directors, err := fetcher.Directors()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if len(directors) == 0 {
		return snippets, nil
	}

	for _, d := range directors {
		// The .retries property enables only for random director
		// https://www.fastly.com/documentation/reference/vcl/declarations/director/
		if DirectorType(d.Type) != Random {
			d.Retries = 0 // zero won't be rendered in the template
		}

		snip, err := renderDirector(d, false)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		snippets = append(snippets, *snip)
	}

	return snippets, nil
}

func renderBackendShields(backends []*Backend) ([]Item, error) {
	shieldDirectors := make(map[string]struct{})
	for _, b := range backends {
		if b.Shield != nil && *b.Shield != "" {
			shieldDirectors[*b.Shield] = struct{}{}
		}
	}

	var snippets []Item
	for sd := range shieldDirectors {
		d := &Director{
			Name:     "ssl_shield_" + strings.ReplaceAll(sd, "-", "_"),
			Type:     int(Shield),
			Backends: []string{},
		}
		snip, err := renderDirector(d, true)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		snippets = append(snippets, *snip)
	}

	return snippets, nil
}

func fetchVCLSnippets(fetcher Fetcher) (ScopedSnippets, IncludeSnippets, error) {
	snippets, err := fetcher.Snippets()
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	// Sort by priority
	sort.Slice(snippets, func(i, j int) bool {
		return snippets[i].Priority < snippets[j].Priority
	})

	scoped := ScopedSnippets{}
	include := IncludeSnippets{}
	for _, snip := range snippets {
		// "none" type means that user could include the snippet arbitrary
		if snip.Type == "none" {
			include[snip.Name] = Item{
				Name:     snip.Name,
				Data:     snip.Content,
				Priority: snip.Priority,
			}
			continue
		}
		// Otherwise, factory with type (phase) name
		if _, ok := scoped[snip.Type]; !ok {
			scoped[snip.Type] = []Item{}
		}
		scoped[snip.Type] = append(scoped[snip.Type], Item{
			Name:     snip.Name,
			Data:     snip.Content,
			Priority: snip.Priority,
		})
	}

	return scoped, include, nil
}

func fetchConditions(fetcher Fetcher) (map[string]*Condition, error) {
	conditions, err := fetcher.Conditions()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	ret := make(map[string]*Condition)
	for _, cond := range conditions {
		ret[cond.Name] = cond
	}
	return ret, nil
}

type BackendInfo struct {
	Name               string
	ConditionName      string
	ConditionStatement string
	Priority           int64 // Add priority for proper ordering
}

func generateBackendSelectionSnippets(fetcher Fetcher) ([]Item, error) {
	// Get backends and conditions
	backends, err := fetcher.Backends()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	conditions, err := fetcher.Conditions()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Create a map of condition names to conditions for REQUEST type only
	conditionMap := make(map[string]*Condition)
	for _, c := range conditions {
		if c.Type == RequestPhase {
			conditionMap[c.Name] = c
		}
	}

	// Filter backends that have request conditions and resolve them
	var conditionBackends []BackendInfo
	var nonConditionBackends []string
	for _, b := range backends {
		if b.RequestCondition != nil && *b.RequestCondition != "" {
			if condition, exists := conditionMap[*b.RequestCondition]; exists {
				conditionBackends = append(conditionBackends, BackendInfo{
					Name:               b.Name,
					ConditionName:      condition.Name,
					ConditionStatement: condition.Statement,
					Priority:           condition.Priority,
				})
			}
		} else {
			nonConditionBackends = append(nonConditionBackends, b.Name)
		}
	}

	// "If there are no backends with conditions that match the request,
	// then the backend without any conditions is chosen.
	// If there are multiple such backends, one is chosen arbitrarily."
	// https://www.fastly.com/documentation/reference/api/services/backend/
	// sort for consistency. Descending alphabetical order since last assignment wins
	sort.Slice(nonConditionBackends, func(i, j int) bool {
		return nonConditionBackends[i] > nonConditionBackends[j]
	})

	// "If multiple backends are defined, the backend that is used for a request
	// is the one with the highest-priority condition attached to it,
	// out of all conditions that this request satisfies.
	// If multiple conditions match the request with the same (highest) priority,
	// one is chosen arbitrarily."
	// https://www.fastly.com/documentation/reference/api/services/backend/
	// Sort backends by priority ascending, then by name descending for consistency
	// so that the last assignment in an "if" will win
	sort.Slice(conditionBackends, func(i, j int) bool {
		if conditionBackends[i].Priority != conditionBackends[j].Priority {
			return conditionBackends[i].Priority < conditionBackends[j].Priority
		}
		return conditionBackends[i].Name > conditionBackends[j].Name
	})

	// Render the backend selection VCL
	return renderBackendSelection(conditionBackends, nonConditionBackends)
}

func renderBackendSelection(conditionBackends []BackendInfo, nonConditionBackends []string) ([]Item, error) {
	buf := pool.Get().(*bytes.Buffer) // nolint:errcheck
	defer pool.Put(buf)

	buf.Reset()
	data := struct {
		Backends             []BackendInfo
		NonConditionBackends []string
	}{
		Backends:             conditionBackends,
		NonConditionBackends: nonConditionBackends,
	}

	if err := backendSelectionTemplate.Execute(buf, data); err != nil {
		return nil, errors.WithStack(err)
	}

	return []Item{{
		Name: "Falco.BackendSelection",
		Data: buf.String(),
	}}, nil
}
