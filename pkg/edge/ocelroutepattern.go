package edge

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

const maxRune = 0x10FFFF

type runeRange struct{ lo, hi rune }

type runeSet []runeRange

type patternItem struct {
	set       runeSet
	mandatory bool
	unbounded bool
	zeroWidth bool
}

type patternQuantifier struct {
	mandatory bool
	unbounded bool
}

type patternReader struct {
	src []rune
	pos int
}

var (
	digitRunes = runeSet{{'0', '9'}}
	wordRunes  = runeSet{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}}
	allRunes   = runeSet{{0, maxRune}}
)

func refuseOverlappingRepeats(src string) error {
	reader := &patternReader{src: []rune(src)}
	_, err := reader.readAlternation()
	return err
}

func mergeRunes(ranges runeSet) runeSet {
	sorted := slices.Clone(ranges)
	slices.SortFunc(sorted, func(a, b runeRange) int { return int(a.lo) - int(b.lo) })
	var merged runeSet
	for _, r := range sorted {
		if last := len(merged) - 1; last >= 0 && r.lo <= merged[last].hi+1 {
			merged[last].hi = max(merged[last].hi, r.hi)
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

func unionRunes(a, b runeSet) runeSet {
	return mergeRunes(append(slices.Clone(a), b...))
}

func invertRunes(s runeSet) runeSet {
	var inverted runeSet
	next := rune(0)
	for _, r := range mergeRunes(s) {
		if r.lo > next {
			inverted = append(inverted, runeRange{next, r.lo - 1})
		}
		next = r.hi + 1
	}
	if next <= maxRune {
		inverted = append(inverted, runeRange{next, maxRune})
	}
	return inverted
}

func intersectRunes(a, b runeSet) runeSet {
	var shared runeSet
	for _, x := range a {
		for _, y := range b {
			if lo, hi := max(x.lo, y.lo), min(x.hi, y.hi); lo <= hi {
				shared = append(shared, runeRange{lo, hi})
			}
		}
	}
	return mergeRunes(shared)
}

func (r *patternReader) readAlternation() ([][]patternItem, error) {
	var branches [][]patternItem
	for {
		branch, err := r.readSequence()
		if err != nil {
			return nil, err
		}
		branches = append(branches, branch)
		if r.pos >= len(r.src) || r.src[r.pos] != '|' {
			return branches, nil
		}
		r.pos++
	}
}

func (r *patternReader) readSequence() ([]patternItem, error) {
	var items []patternItem
	for r.pos < len(r.src) && r.src[r.pos] != '|' && r.src[r.pos] != ')' {
		atom, err := r.readAtom()
		if err != nil {
			return nil, err
		}
		items = append(items, atom...)
	}
	return items, refuseOverlappingItems(items)
}

func (r *patternReader) readAtom() ([]patternItem, error) {
	c := r.src[r.pos]
	r.pos++
	switch {
	case c == '(':
		return r.readGroup()
	case c == '[':
		set, err := r.readClass()
		if err != nil {
			return nil, err
		}
		return []patternItem{r.readQuantified(set)}, nil
	case c == '.':
		return []patternItem{r.readQuantified(allRunes)}, nil
	case c == '^' || c == '$':
		return []patternItem{{zeroWidth: true}}, nil
	case c == '\\' && r.pos < len(r.src):
		escaped := r.src[r.pos]
		r.pos++
		if escaped == 'b' || escaped == 'B' {
			return []patternItem{{zeroWidth: true}}, nil
		}
		return []patternItem{r.readQuantified(escapedRunes(escaped))}, nil
	}
	return []patternItem{r.readQuantified(runeSet{{c, c}})}, nil
}

func (r *patternReader) readGroup() ([]patternItem, error) {
	if r.pos+1 < len(r.src) && r.src[r.pos] == '?' && r.src[r.pos+1] == ':' {
		r.pos += 2
	}
	branches, err := r.readAlternation()
	if err != nil {
		return nil, err
	}
	if r.pos >= len(r.src) || r.src[r.pos] != ')' {
		return nil, errors.New("a pattern closes every group")
	}
	r.pos++
	quantifier := r.readQuantifier()
	if len(branches) == 1 && !quantifier.unbounded {
		items := branches[0]
		if !quantifier.mandatory {
			items = slices.Clone(items)
			for i := range items {
				items[i].mandatory = false
			}
		}
		return items, nil
	}
	opaque := patternItem{mandatory: quantifier.mandatory, unbounded: quantifier.unbounded, zeroWidth: true}
	for _, branch := range branches {
		holdsMandatory := false
		for _, item := range branch {
			opaque.set = unionRunes(opaque.set, item.set)
			opaque.unbounded = opaque.unbounded || item.unbounded
			opaque.zeroWidth = opaque.zeroWidth && item.zeroWidth
			holdsMandatory = holdsMandatory || item.mandatory && !item.zeroWidth
		}
		opaque.mandatory = opaque.mandatory && holdsMandatory
	}
	return []patternItem{opaque}, nil
}

func (r *patternReader) readQuantified(set runeSet) patternItem {
	quantifier := r.readQuantifier()
	return patternItem{set: set, mandatory: quantifier.mandatory, unbounded: quantifier.unbounded}
}

func (r *patternReader) readQuantifier() patternQuantifier {
	if r.pos >= len(r.src) {
		return patternQuantifier{mandatory: true}
	}
	quantifier := patternQuantifier{mandatory: true}
	switch r.src[r.pos] {
	case '*':
		quantifier = patternQuantifier{unbounded: true}
		r.pos++
	case '+':
		quantifier = patternQuantifier{mandatory: true, unbounded: true}
		r.pos++
	case '?':
		quantifier = patternQuantifier{}
		r.pos++
	case '{':
		bounds := repeatBound.FindStringSubmatch(string(r.src[r.pos:]))
		if bounds == nil {
			return quantifier
		}
		least, _ := strconv.Atoi(bounds[1])
		quantifier = patternQuantifier{mandatory: least > 0, unbounded: strings.Contains(bounds[0], ",") && bounds[2] == ""}
		r.pos += len([]rune(bounds[0]))
	default:
		return quantifier
	}
	if r.pos < len(r.src) && r.src[r.pos] == '?' {
		r.pos++
	}
	return quantifier
}

func escapedRunes(c rune) runeSet {
	switch c {
	case 'd':
		return digitRunes
	case 'D':
		return invertRunes(digitRunes)
	case 'w':
		return wordRunes
	case 'W':
		return invertRunes(wordRunes)
	case 'n':
		return runeSet{{'\n', '\n'}}
	case 'r':
		return runeSet{{'\r', '\r'}}
	case 't':
		return runeSet{{'\t', '\t'}}
	}
	return runeSet{{c, c}}
}

func (r *patternReader) readClass() (runeSet, error) {
	negated := r.pos < len(r.src) && r.src[r.pos] == '^'
	if negated {
		r.pos++
	}
	var members runeSet
	for r.pos < len(r.src) && r.src[r.pos] != ']' {
		c := r.src[r.pos]
		r.pos++
		member := runeSet{{c, c}}
		if c == '\\' && r.pos < len(r.src) {
			member = escapedRunes(r.src[r.pos])
			r.pos++
			if len(member) != 1 || member[0].lo != member[0].hi {
				members = unionRunes(members, member)
				continue
			}
			c = member[0].lo
		}
		if r.pos+1 < len(r.src) && r.src[r.pos] == '-' && r.src[r.pos+1] != ']' {
			r.pos++
			hi := r.src[r.pos]
			r.pos++
			if hi == '\\' && r.pos < len(r.src) {
				escaped := escapedRunes(r.src[r.pos])
				r.pos++
				if len(escaped) != 1 || escaped[0].lo != escaped[0].hi {
					return nil, errors.New("a pattern ends no class range with a class escape")
				}
				hi = escaped[0].lo
			}
			if hi < c {
				return nil, errors.New("a pattern orders every class range")
			}
			member = runeSet{{c, hi}}
		}
		members = unionRunes(members, member)
	}
	if r.pos >= len(r.src) {
		return nil, errors.New("a pattern closes every class")
	}
	r.pos++
	if negated {
		return invertRunes(members), nil
	}
	return mergeRunes(members), nil
}

func refuseOverlappingItems(items []patternItem) error {
	for i, x := range items {
		if !x.unbounded || x.zeroWidth {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			w := items[j]
			if !w.unbounded || w.zeroWidth {
				continue
			}
			shared := intersectRunes(x.set, w.set)
			if len(shared) > 0 && !hasSeparator(items[i+1:j], shared) {
				return errors.New("a pattern repeats two parts without bound that can match the same characters, with no required character between them that one of them cannot match")
			}
		}
	}
	return nil
}

func hasSeparator(between []patternItem, shared runeSet) bool {
	return slices.ContainsFunc(between, func(item patternItem) bool {
		return item.mandatory && !item.zeroWidth && len(intersectRunes(item.set, shared)) == 0
	})
}
