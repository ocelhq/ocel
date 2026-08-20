package origin

type Selection struct {
	Kind    Kind
	Options []byte
}

type Topology struct {
	Origin    Selection
	Edge      Selection
	DNS       Selection
	Resources map[Role]Selection
}

type Requirement struct {
	Role Role
	Name string
}

type Requirements struct {
	Resources []Requirement
}

type Catalog struct {
	Origin   Facts
	Backings map[Role]map[Kind]BackingFacts
}

func BuildCatalog(o Origin, independent ...Backing) Catalog {
	c := Catalog{Origin: o.Facts(), Backings: map[Role]map[Kind]BackingFacts{}}
	for _, b := range append(o.Native(), independent...) {
		f := b.Facts()
		if c.Backings[f.Role] == nil {
			c.Backings[f.Role] = map[Kind]BackingFacts{}
		}
		c.Backings[f.Role][f.Kind] = f
	}
	return c
}
