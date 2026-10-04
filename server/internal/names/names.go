// Package names gives each phone a friendly generated name and colour
// ("Blue Otter") so a person can find their dot on the big screen. The name
// is derived from the random session id alone: nothing is asked for and
// nothing personal goes into it.
package names

import "hash/fnv"

// Name is a generated name and its colour (CSS hex).
type Name struct {
	Name  string
	Color string
}

type colour struct{ name, hex string }

// Colours are mid-tones that read on both a dark and a light map, and stay
// clear of the status colours' exact hues.
var colours = []colour{
	{"Blue", "#3b82f6"},
	{"Purple", "#a855f7"},
	{"Pink", "#ec4899"},
	{"Orange", "#f97316"},
	{"Teal", "#14b8a6"},
	{"Lime", "#84cc16"},
	{"Cyan", "#06b6d4"},
	{"Violet", "#7c3aed"},
	{"Coral", "#fb7185"},
	{"Gold", "#d4a017"},
}

var animals = []string{
	"Otter", "Fox", "Heron", "Lynx", "Panda", "Koala", "Robin", "Tiger", "Whale", "Gecko",
	"Bison", "Crane", "Finch", "Hare", "Ibis", "Moose", "Newt", "Orca", "Puffin", "Quail",
	"Raven", "Seal", "Tapir", "Wolf", "Yak", "Zebra", "Badger", "Camel", "Dingo", "Eagle",
	"Ferret", "Goose", "Hippo", "Jaguar", "Lemur", "Marten", "Ocelot", "Pika", "Stoat", "Wombat",
}

// Count is how many different names there are.
func Count() int { return len(colours) * len(animals) }

func hash(id string) (c, a int) {
	h := fnv.New64a()
	h.Write([]byte(id))
	v := h.Sum64()
	return int(v % uint64(len(colours))), int((v >> 20) % uint64(len(animals)))
}

func at(c, a int) Name {
	col := colours[c%len(colours)]
	return Name{Name: col.name + " " + animals[a%len(animals)], Color: col.hex}
}

// For is the name of a session id: always the same for the same id.
func For(id string) Name {
	c, a := hash(id)
	return at(c, a)
}

// Pick is For(id), stepped to the next free one when it would clash with a
// name or colour already in use: first a colour nobody has (so a handful
// of people each get their own colour), else the same colour with the next
// free animal. used reports whether a name / a colour is taken. With every
// name taken it returns For(id).
func Pick(id string, usedName, usedColor func(string) bool) Name {
	c, a := hash(id)
	for i := 0; i < len(colours); i++ {
		if n := at(c+i, a); !usedColor(n.Color) && !usedName(n.Name) {
			return n
		}
	}
	for j := 0; j < len(animals); j++ {
		for i := 0; i < len(colours); i++ {
			if n := at(c+i, a+j); !usedName(n.Name) {
				return n
			}
		}
	}
	return at(c, a)
}
