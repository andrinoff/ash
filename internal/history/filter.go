package history

import "strings"

// FuzzyFilter is Fuzzy over the entries keep accepts: the commands that
// contain the runes of q in order, ranked as Fuzzy ranks them, and at most
// limit of them if limit is positive. With an empty q it returns the most
// recent entries keep accepts. A nil keep accepts every entry.
//
// keep runs once for each command, before it is matched, while the store
// is locked for reading, so it should be quick and must not use the store.
// InDir, Under and Failed make the usual filters.
func (s *Store) FuzzyFilter(q string, limit int, keep func(Entry) bool) []Match {
	if keep == nil {
		return s.Fuzzy(q, limit)
	}
	<-s.loaded
	s.mu.RLock()
	defer s.mu.RUnlock()
	if q == "" {
		var out []Match
		if limit > 0 {
			out = make([]Match, 0, min(limit, len(s.items)-s.dead))
		}
		for i := len(s.items) - 1; i >= 0; i-- {
			it := &s.items[i]
			if it.cmd == "" {
				continue
			}
			if e := it.entry(); keep(e) {
				out = append(out, Match{Entry: e})
				if len(out) == limit {
					break
				}
			}
		}
		return out
	}
	m := newFuzzyMatcher(q)
	r := ranking{limit: limit}
	for i := len(s.items) - 1; i >= 0; i-- {
		it := &s.items[i]
		if it.cmd == "" || !keep(it.entry()) {
			continue
		}
		if score, _, ok := m.match(it.cmd, false); ok {
			r.add(hit{int32(score), int32(i)})
		}
	}
	hits := r.sorted()
	out := make([]Match, len(hits))
	for k, h := range hits {
		it := &s.items[h.idx]
		_, pos, _ := m.match(it.cmd, true)
		out[k] = Match{Entry: it.entry(), Score: int(h.score), Positions: pos}
	}
	return out
}

// InDir keeps the commands last run in dir.
func InDir(dir string) func(Entry) bool {
	return func(e Entry) bool { return e.Dir == dir }
}

// Under keeps the commands last run in root or in a directory below it,
// such as anywhere in a repository. root is an absolute path.
func Under(root string) func(Entry) bool {
	root = strings.TrimRight(root, "/")
	prefix := root + "/"
	if root == "" {
		root = "/"
	}
	return func(e Entry) bool {
		return e.Dir == root || strings.HasPrefix(e.Dir, prefix)
	}
}

// Failed keeps the commands whose last run failed.
func Failed(e Entry) bool { return e.Status != 0 }
