// Package plan implements the trial plan catalog: it validates and locks
// immutable plans (specimen, sensors, thresholds, calibration summary, stage
// dependencies) and rejects dependency cycles, duplicate sensors, stale
// calibration summaries, dangling dependencies and uncovered stages before any
// plan version is produced.
package plan

import (
	"context"
	"sort"

	"thermal-vacuum-test-gate/internal/domain"
)

// Store is the persistence boundary for locked plans.
type Store interface {
	SavePlan(ctx context.Context, p domain.TestPlan) error
	GetPlan(ctx context.Context, id string) (domain.TestPlan, error)
}

// Clock returns the current logical time in milliseconds.
type Clock func() int64

// Catalog validates and locks immutable test plans.
type Catalog struct {
	store Store
	now   Clock
}

// NewCatalog builds a catalog backed by the given store. now may be nil, in
// which case stale-calibration checks are skipped.
func NewCatalog(store Store) *Catalog {
	return &Catalog{store: store}
}

// SetClock wires a logical clock for stale-calibration validation.
func (c *Catalog) SetClock(now Clock) *Catalog { c.now = now; return c }

// Get returns a locked plan by id.
func (c *Catalog) Get(ctx context.Context, id string) (domain.TestPlan, error) {
	return c.store.GetPlan(ctx, id)
}

// LockPlan validates and persists an immutable plan version. On validation
// failure it returns a *domain.Error whose Reasons are in deterministic order
// and no plan version is produced.
func (c *Catalog) LockPlan(ctx context.Context, in domain.TestPlan) (domain.TestPlan, error) {
	if err := c.validate(in); err != nil {
		return domain.TestPlan{}, err
	}
	in.Version = 1
	if c.now != nil {
		in.LockedAtMillis = c.now()
	}
	if err := c.store.SavePlan(ctx, in); err != nil {
		return domain.TestPlan{}, err
	}
	return in, nil
}

// validate checks structural and numeric invariants before locking.
func (c *Catalog) validate(p domain.TestPlan) error {
	var reasons []string

	if p.SpecimenID == "" {
		reasons = append(reasons, "specimen identifier is required")
	}
	if p.CalibrationSummary == "" {
		reasons = append(reasons, "calibration summary is required")
	}
	if c.now != nil && p.CalibrationValidUntilMillis != 0 && p.CalibrationValidUntilMillis < c.now() {
		reasons = append(reasons, "calibration summary is stale")
	}
	if p.Cycles <= 0 {
		reasons = append(reasons, "cycle count must be positive")
	}
	reasons = append(reasons, duplicateSensors(p)...)
	reasons = append(reasons, dependencyCycle(p)...)
	reasons = append(reasons, danglingDependencies(p)...)
	reasons = append(reasons, uncoveredStages(p)...)
	reasons = append(reasons, invalidRanges(p)...)

	if len(reasons) > 0 {
		return domain.NewError(domain.CodeInvalidRange, "plan validation failed").WithReasons(reasons...)
	}
	return nil
}

// duplicateSensors rejects repeated sensor IDs in deterministic order.
func duplicateSensors(p domain.TestPlan) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Sensors {
		if s.ID == "" {
			continue
		}
		if seen[s.ID] {
			out = append(out, "duplicate sensor: "+s.ID)
		}
		seen[s.ID] = true
	}
	sort.Strings(out)
	return out
}

// dependencyCycle detects a cycle in the locked stage dependency graph using
// deterministic depth-first search.
func dependencyCycle(p domain.TestPlan) []string {
	index := map[domain.StageName]int{}
	for i, s := range p.Stages {
		index[s.Name] = i
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, len(p.Stages))
	var cycle []string
	var visit func(i int)
	visit = func(i int) {
		color[i] = gray
		for _, dep := range p.Stages[i].Dependencies {
			j, ok := index[dep]
			if !ok {
				continue
			}
			switch color[j] {
			case gray:
				cycle = append(cycle, "dependency cycle: "+string(p.Stages[j].Name))
			case white:
				visit(j)
			}
		}
		color[i] = black
	}
	for i := range p.Stages {
		if color[i] == white {
			visit(i)
		}
	}
	sort.Strings(cycle)
	return cycle
}

// danglingDependencies rejects dependencies that reference a stage not present
// in the plan.
func danglingDependencies(p domain.TestPlan) []string {
	index := map[domain.StageName]bool{}
	for _, s := range p.Stages {
		index[s.Name] = true
	}
	var out []string
	for _, s := range p.Stages {
		for _, dep := range s.Dependencies {
			if !index[dep] {
				out = append(out, "uncovered dependency: "+string(dep))
			}
		}
	}
	sort.Strings(out)
	return out
}

// uncoveredStages rejects stages whose name is outside the canonical workflow.
// The run engine can only ever reach the fixed canonical stage set, so a plan
// that lists any other stage has an uncovered (unreachable) stage.
func uncoveredStages(p domain.TestPlan) []string {
	canonical := map[domain.StageName]bool{}
	for _, s := range domain.CanonicalStages {
		canonical[s] = true
	}
	var out []string
	for _, s := range p.Stages {
		if !canonical[s.Name] {
			out = append(out, "uncovered stage: "+string(s.Name))
		}
	}
	sort.Strings(out)
	return out
}

// invalidRanges rejects out-of-range fixed-point targets. A stage with no
// temperature targets (e.g. evacuation or repressurisation) skips the
// cold/hot ordering check; a stage that does specify temperatures must have
// a cold target strictly below its hot target.
func invalidRanges(p domain.TestPlan) []string {
	var out []string
	for _, s := range p.Stages {
		if s.VacuumTargetMilliPa < 0 {
			out = append(out, "invalid vacuum range for stage: "+string(s.Name))
			continue
		}
		if (s.ColdTargetMilliKelvin != 0 || s.HotTargetMilliKelvin != 0) &&
			s.ColdTargetMilliKelvin >= s.HotTargetMilliKelvin {
			out = append(out, "invalid temperature range for stage: "+string(s.Name))
		}
	}
	sort.Strings(out)
	return out
}
